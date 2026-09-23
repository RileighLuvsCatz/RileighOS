# Deploy rileighos to Raspberry Pi 4 over Tailscale

Target: Pi 4 (2 GB, 64-bit Pi OS) serves SQLite from `/var/lib/rileighos/rileighos.db`;
laptop + PC talk to it over the tailnet. No port-forwarding, no cloud.

## 0. Prerequisites

- Pi OS already flashed (done). Boot the Pi, complete first-boot user setup.
  Note your login user (`whoami`) — if it is not `pi`, edit `User=` in
  `rileighos.service` to match.
- This repo builds with a pure-Go SQLite driver (`modernc.org/sqlite`,
  see `go.mod`), so cross-compiling from your laptop works with no
  toolchain changes.

## 1. First boot + timezone (required)

Streaks use the **server-local date** (`internal/store/checkoff.go: Today()`). If the Pi's
timezone is wrong, `today` and streaks are wrong.

```sh
# on the Pi
sudo hostnamectl set-hostname rileighos-pi
sudo timedatectl set-timezone America/Chicago  # <-- put YOUR zone here
timedatectl  # confirm Local time + Time zone are correct
```

## 2. Tailscale on all three machines

```sh
# on the Pi, laptop, and PC (same tailnet account)
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
tailscale status
tailscale ping rileighos-pi  # or: ping <Pi tailnet IP>
```

Note the Pi's tailnet name/IP — that becomes your server URL, e.g.
`http://rileighos-pi:8080` or `http://100.x.y.z:8080`.

## 3. Build for Pi 4 (arm64) from your laptop

```sh
# from repo root
GOOS=linux GOARCH=arm64 go build -o rileighos ./cmd/rileighos
file rileighos  # expect: ELF 64-bit LSB executable, ARM aarch64
```

Pi 4 is Cortex-A72 running 64-bit Pi OS → `GOARCH=arm64` is correct.
(Only 32-bit Pi OS / Pi Zero would need `GOARCH=arm`.)

## 4. Install binary + systemd unit on the Pi

```sh
# from repo root (laptop), replace <pi> with tailnet name/IP
scp rileighos <pi>:/tmp/rileighos
scp deploy/rileighos.service <pi>:/tmp/rileighos.service

# on the Pi
sudo install -m 0755 /tmp/rileighos /usr/local/bin/rileighos
sudo install -m 0644 /tmp/rileighos.service /etc/systemd/system/rileighos.service
# if your Pi user is not `pi`: sudo systemctl edit rileighos  # override User=
sudo systemctl daemon-reload
sudo systemctl enable --now rileighos
systemctl status rileighos --no-pager
journalctl -u rileighos -f  # startup line: listening on http://localhost:8080 (backend sqlite)
```

What the unit does (`deploy/rileighos.service`):

- `ExecStart=/usr/local/bin/rileighos serve --backend sqlite --db /var/lib/rileighos/rileighos.db --addr :8080`
- `StateDirectory=rileighos` creates `/var/lib/rileighos` owned by the
  service user — no home-directory path hardcoded, works on any systemd
  distro. Override the DB with `RILEIGHOS_DB_PATH` via `systemctl edit` if needed.
- `--addr :8080` (the new default) listens on all interfaces so the
  tailnet can reach it. For local-only dev, run
  `rileighos serve --addr localhost:8080` instead.

## 5. Point the CLI at the Pi

No code change — the CLI is already a pure HTTP client (`internal/client/client.go`):

```sh
export RILEIGHOS_SERVER_URL=http://rileighos-pi:8080
# or per-command: rileighos --server http://rileighos-pi:8080 todo list
curl http://rileighos-pi:8080/healthz  # expect: {"ok":true}
rileighos todo list
```

## 6. Phase 3 done-check (both machines, same truth)

```sh
# laptop
rileighos todo add "from laptop"
rileighos checkoff add "exercise"
# PC (same RILEIGHOS_SERVER_URL)
rileighos todo list        # sees "from laptop"
rileighos checkoff list
rileighos today            # open todos + streaks in one call
```

Pass = both machines see identical todos/notes served from the Pi.

## Troubleshooting

| Symptom | Check |
|---|---|
| `cannot reach server ... try rileighos serve` | server down? `systemctl status rileighos`, `journalctl -u rileighos`, wrong `RILEIGHOS_SERVER_URL`? |
| Connection refused over tailnet, works on Pi | `--addr` must be `:8080`, not `localhost:8080`; `ss -tlnp \| grep 8080` should show `*:8080` |
| `tailscale ping` fails | `tailscale status` on all three; same account/tailnet; `sudo tailscale up` again |
| Streaks off by a day | `timedatectl` zone on Pi ≠ yours; restart service after fixing |
| Permission denied on DB | `User=` in unit ≠ Pi login user; `/var/lib/rileighos` ownership; `journalctl` shows path |
| Wrong arch `cannot execute binary file` | rebuilt without `GOOS=linux GOARCH=arm64`? `file rileighos` must say `aarch64` |

## Updating

```sh
GOOS=linux GOARCH=arm64 go build -o rileighos ./cmd/rileighos
scp rileighos <pi>:/tmp/rileighos
# on Pi: sudo install -m 0755 /tmp/rileighos /usr/local/bin/rileighos && sudo systemctl restart rileighos
```

SQLite file stays at `/var/lib/rileighos/rileighos.db` — binary swaps never touch data.
