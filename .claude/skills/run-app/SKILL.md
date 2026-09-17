---
name: run-app
description: How to build, run, test and smoke-check the ClaudePulse web app locally (port 3333). Use when asked to run/start the app, verify a change end-to-end, or when the /run skill needs a project-specific launch recipe.
---

# Running ClaudePulse

## Build & run

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/claudepulse.exe ./cmd/claudepulse   # single static binary
./bin/claudepulse.exe                                  # serves http://127.0.0.1:3333
```

Dev loop (no binary): `go run ./cmd/claudepulse`.

Flags / env (flag wins over env, env wins over default):

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-addr` | `CP_ADDR` | `127.0.0.1:3333` | Listen address. Always loopback — never `0.0.0.0`. |
| `-claude-dir` | `CP_CLAUDE_DIR` | `$HOME/.claude` (`%USERPROFILE%\.claude` on Windows) | Directory to monitor |
| `-db` | `CP_DB` | `./data/claudepulse.db` | SQLite file (created on first run) |
| `-scan-interval` | `CP_SCAN_INTERVAL` | `30s` | Safety-net rescan cadence; fsnotify triggers scans within ~1 s of a change |
| `-log-level` | `CP_LOG_LEVEL` | `info` | `debug` prints every file indexed |
| `-reset-db` | — | off | Delete the DB and re-index |
| `-version` | — | — | Print the build version |

Append `?theme=light` (or `dark`) to any URL to force a theme for screenshots.

Port **3333** is the user's choice. If it is busy the app must exit with a
clear error, not silently pick another port.

## Smoke check after a change

```bash
go vet ./... && go test ./... -count=1
go run ./cmd/claudepulse -log-level debug &   # in background
curl -s http://127.0.0.1:3333/healthz            # expect {"status":"ok","indexed_sessions":N}
curl -s http://127.0.0.1:3333/api/v1/summary | head -c 400
```

Then open `http://127.0.0.1:3333/` in a browser (or the `claude-in-chrome` skill
for a screenshot) and confirm the dashboard renders numbers, not zeros.

## Running against a fixture instead of the real ~/.claude

`go run ./cmd/claudepulse -claude-dir ./testdata/claude-home -db ./data/test.db`
— `testdata/claude-home` is a scrubbed miniature copy of the real layout and is
the safe way to develop UI without touching live data.
