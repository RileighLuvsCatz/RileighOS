# Why client/server + Pi + Tailscale (pre-fair writeup)

## The 4 sentences

Rileighos keeps one `Store` interface (`internal/store/store.go`) with interchangeable JSON
and SQLite backends, so storage changes are a one-line swap in `openStore`
instead of a rewrite. A plain-`net/http` server wraps that store and the CLI
is a pure HTTP client (`internal/client/client.go`), which proved the client/server pattern on
localhost before any real networking existed. That same server binary moves to
a Raspberry Pi 4 holding the single SQLite file, with Tailscale providing a
private tailnet so every device shares one source of truth without
port-forwarding or a cloud account. Scoping calendar OAuth out of the pre-fair
build was deliberate: OAuth is the biggest time risk and least demo-critical
piece, while check-offs, streaks, and the one-call `GET /today` already prove
server-side features work end to end.

## Interview talking points (one per phase)

- **Phase 1 — Foundation:** structs, `Store`/`TodoStore`/`NoteStore`/
  `CheckoffStore` interfaces, table-driven tests on both backends
  (`internal/store/store_test.go`, `internal/store/checkoff_test.go`). "I can say I know Go, not just Java."
- **Phase 2 — Client/server split:** boring, consistent JSON shapes
  (`internal/server/server.go` route table), `PATCH /todos/{id}` with `{"done": bool}`,
  `ErrNotFound` preserved across HTTP (`internal/client/client.go`), friendly
  "is it running?" error instead of bare connection-refused
  (`TestClientServerDown`). "Designed and deployed a client/server architecture."
- **Phase 3 — Pi + Tailscale:** pure-Go SQLite cross-compiles with
  `GOOS=linux GOARCH=arm64`; `--addr :8080` default listens on all interfaces
  for the tailnet; `StateDirectory=rileighos` keeps the DB at
  `/var/lib/rileighos/rileighos.db` with no hardcoded home dir; Pi timezone
  set explicitly because `Today()` is server-local. "Same binary, different
  physical location — networking isolated from everything else."
- **Phase 4 (descoped) — Check-offs/streaks/today:** `Checkoff` separate from
  `Todo`, days stored as `YYYY-MM-DD`, streaks derived never stored
  (`internal/store/checkoff.go: CurrentStreak`), idempotent check/uncheck
  (`INSERT OR IGNORE`), `GET /today` answers "what does my day look like" in
  one call. "Built server-side features by default once the pattern was proven."

## What was cut and why (say this before they ask)

Google Calendar OAuth + Canvas sync are deferred post-fair. OAuth flows are
notoriously slow to get right, Canvas token availability varies by school
(FERPA restrictions → OAuth2 fallback), and neither is needed to demo the
architecture. The sync-job design (map assignments → todos, diff against
already-imported to avoid dupes) is planned, not started.
