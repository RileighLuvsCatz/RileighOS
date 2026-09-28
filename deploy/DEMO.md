# Career-fair demo (5 minutes, descoped Phases 1–4)

Goal: show CLI → local server → Pi server progression, ending on the one-call
`today` story. All commands below already exist — nothing here needs new code.

## Setup (before anyone walks up)

```sh
# terminal 1: server (local stand-in for the Pi until Phase 3 is live)
rileighos serve --backend sqlite --db /tmp/demo.db --addr localhost:8080
# terminal 2: client
export RILEIGHOS_SERVER_URL=http://localhost:8080
# on the Pi at home: same, with http://rileighos-pi:8080
```

## Script

```sh
# 1. Foundation (Phase 1+2 in one breath)
rileighos todo add "write fair one-pager"
rileighos note add "ask about embedded roles"
rileighos todo list
# say: "CLI never touches storage — every command is HTTP to localhost."

# 2. Check-offs + streaks (Phase 4 descoped)
rileighos checkoff add "exercise"
rileighos checkoff check 1            # no date = today
rileighos checkoff list               # [x] with streak 1
rileighos checkoff check 1 2026-09-22 # backfill yesterday → streak 2
rileighos checkoff show 1

# 3. Plan work before the deadline, then see the whole day in one call
rileighos todo plan 1
rileighos today
# overdue work/deadlines, planned work, due today, and check-offs. Server assembles it
# (GET /today), so CLI, TUI, and future GUI all share the answer.

# 4. Canvas import (the day-to-day hook, needs RILEIGHOS_CANVAS_TOKEN on server)
rileighos canvas sync
# first run asks per course: [y]es / [n]o / [Enter] later. Excluding later
# converts already-imported items to plain local todos — nothing is deleted.
rileighos canvas courses            # gating state: active / excluded / pending
rileighos today                     # synced assignments due today show up here
# say: "submitted upstream marks the todo done automatically — manual sync,
# on-open sync, and a server ticker all share one idempotent path."

# 5. Error handling (30 seconds, memorable)
kill %1  # stop server  — or: RILEIGHOS_SERVER_URL=http://localhost:9999 rileighos todo list
rileighos todo list
# expect: "cannot reach server at ... (is it running? try `rileighos serve`)"
# restart server, continue.
```

## If they ask "where's the Pi?"

- Live (Phase 3 done): `RILEIGHOS_SERVER_URL=http://rileighos-pi:8080 rileighos today`
  from your laptop — same data as the PC. Point at `deploy/README.md` runbook.
- Not yet: "Server binary is already Pi-ready (`GOOS=linux GOARCH=arm64`,
  pure-Go SQLite, `--addr :8080` default, systemd unit in `deploy/`).
  Tonight it's `scp` + `systemctl enable --now`."

## Seed / reset

```sh
rm -f /tmp/demo.db
rileighos serve --backend sqlite --db /tmp/demo.db --addr localhost:8080 &
rileighos todo add "write fair one-pager"
rileighos checkoff add "exercise"
```

## Don't demo

Calendar OAuth (deferred — say why, see `ARCHITECTURE.md`), TUI, Discord,
trackers. If asked: "OAuth is the biggest time risk, cut from pre-fair scope
by design — Canvas uses a personal token instead, no OAuth involved."
