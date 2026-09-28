#!/usr/bin/env bash
# rileighos-update: self-update the rileighos binary from the latest GitHub Release.
#
# Installed to /usr/local/bin/rileighos-update and run nightly by
# rileighos-update.timer (oneshot via rileighos-update.service).
#
# Behavior:
#   - Fetch the latest release metadata from the GitHub API.
#   - Download the `rileighos-linux-arm64` asset plus its published `.sha256`.
#   - Verify the download against the published checksum.
#   - If the download is byte-identical (sha256) to /usr/local/bin/rileighos,
#     exit 0 quietly (nothing to do).
#   - Otherwise back up the current binary to /usr/local/bin/rileighos.prev,
#     install the new one (0755), restart the rileighos service, and verify it
#     is active. On failure, restore the backup, restart, and exit nonzero.
#
# Human-readable progress goes to stdout/stderr (captured by the journal).
#
# Dependencies (stock Pi OS only): bash, curl, sha256sum, systemctl.
# JSON parsing prefers python3 when present but falls back to grep/sed;
# jq is never required.
set -euo pipefail

REPO="RileighLuvsCatz/RileighOS"
API_URL="https://api.github.com/repos/${REPO}/releases/latest"
ASSET_NAME="rileighos-linux-arm64"
BIN="/usr/local/bin/rileighos"
PREV="/usr/local/bin/rileighos.prev"
UNIT="rileighos"

log() {
	echo "rileighos-update: $*"
}

die() {
	echo "rileighos-update: ERROR: $*" >&2
	exit 1
}

# Fetch the latest-release JSON from the GitHub API.
release_json="$(curl -fsSL --retry 3 --max-time 60 "$API_URL")" \
	|| die "could not fetch release metadata from $API_URL"

# Extract the release tag for logging. Never fatal if it fails.
release_tag=""
if command -v python3 >/dev/null 2>&1; then
	release_tag="$(printf '%s' "$release_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tag_name",""))' 2>/dev/null || true)"
else
	release_tag="$(printf '%s' "$release_json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || true)"
fi
if [ -z "$release_tag" ]; then
	release_tag="(unknown tag)"
fi

# Extract the browser_download_url of the exact asset (trailing quote in the
# match excludes the `rileighos-linux-arm64.sha256` checksum asset).
asset_url=""
if command -v python3 >/dev/null 2>&1; then
	asset_url="$(ASSET="$ASSET_NAME" RELEASE_JSON="$release_json" python3 -c '
import json, os
data = json.loads(os.environ["RELEASE_JSON"])
want = os.environ["ASSET"]
for a in data.get("assets", []):
    if a.get("name") == want:
        print(a.get("browser_download_url", ""))
        break
' 2>/dev/null || true)"
fi
if [ -z "$asset_url" ]; then
	asset_url="$(printf '%s' "$release_json" \
		| grep -o "\"browser_download_url\"[[:space:]]*:[[:space:]]*\"[^\"]*${ASSET_NAME}\"" \
		| head -n1 \
		| sed 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || true)"
fi
[ -n "$asset_url" ] || die "asset ${ASSET_NAME} not found in latest release ${release_tag}"

sha_url="${asset_url}.sha256"
log "latest release is ${release_tag}"

tmpdir="$(mktemp -d)" || die "could not create temp dir"
trap 'rm -rf "$tmpdir"' EXIT

log "downloading ${asset_url}"
curl -fsSL --retry 3 --max-time 300 -o "$tmpdir/$ASSET_NAME" "$asset_url" \
	|| die "could not download ${asset_url}"

log "downloading ${sha_url}"
curl -fsSL --retry 3 --max-time 60 -o "$tmpdir/${ASSET_NAME}.sha256" "$sha_url" \
	|| die "could not download checksum ${sha_url}"

expected="$(awk '{print $1}' "$tmpdir/${ASSET_NAME}.sha256")" \
	|| die "could not parse checksum file"
[ -n "$expected" ] || die "checksum file is empty"
actual="$(sha256sum "$tmpdir/$ASSET_NAME" | awk '{print $1}')"
if [ "$actual" != "$expected" ]; then
	die "checksum mismatch for download (got ${actual}, want ${expected}); refusing to install"
fi
log "checksum verified (sha256 ${actual})"

if [ -x "$BIN" ]; then
	installed="$(sha256sum "$BIN" | awk '{print $1}')"
	if [ "$installed" = "$actual" ]; then
		log "already up to date (${release_tag}, sha256 ${actual}); nothing to do"
		exit 0
	fi
	log "installed binary differs (installed sha256 ${installed}); updating to ${release_tag}"
else
	log "no installed binary at ${BIN}; installing ${release_tag} fresh"
fi

if [ -e "$BIN" ]; then
	log "backing up ${BIN} to ${PREV}"
	cp -a "$BIN" "$PREV" || die "could not back up ${BIN} to ${PREV}"
fi

log "installing new binary to ${BIN}"
install -m 0755 "$tmpdir/$ASSET_NAME" "$BIN" || die "could not install new binary"

rollback() {
	log "update failed; restoring ${PREV} to ${BIN}"
	if [ -e "$PREV" ]; then
		cp -a "$PREV" "$BIN" || log "rollback copy failed; manual recovery needed"
		systemctl restart "$UNIT" || true
	else
		log "no backup at ${PREV}; cannot roll back automatically"
	fi
}

log "restarting ${UNIT}"
if ! systemctl restart "$UNIT"; then
	rollback
	die "systemctl restart ${UNIT} failed; rolled back"
fi
if ! systemctl is-active --quiet "$UNIT"; then
	rollback
	die "${UNIT} is not active after restart; rolled back"
fi

new_version="$("$BIN" version 2>/dev/null || echo '(unknown)')"
log "updated to ${release_tag} (rileighos version: ${new_version}); ${UNIT} is active"
