---
name: run-app
description: How to build, run, test and smoke-check the claude-monitor web app locally (port 48273). Use when asked to run/start the app, verify a change end-to-end, or when the /run skill needs a project-specific launch recipe.
---

# Running claude-monitor

## Build & run

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/claude-monitor.exe ./cmd/claude-monitor   # single static binary
./bin/claude-monitor.exe                                  # serves http://127.0.0.1:48273
```

Dev loop (no binary): `go run ./cmd/claude-monitor`.

Flags / env (flag wins over env, env wins over default):

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-addr` | `CM_ADDR` | `127.0.0.1:48273` | Listen address. Always loopback — never `0.0.0.0`. |
| `-claude-dir` | `CM_CLAUDE_DIR` | `$HOME/.claude` (`%USERPROFILE%\.claude` on Windows) | Directory to monitor |
| `-db` | `CM_DB` | `./data/claude-monitor.db` | SQLite file (created on first run) |
| `-scan-interval` | `CM_SCAN_INTERVAL` | `30s` | Safety-net rescan cadence; fsnotify triggers scans within ~1 s of a change |
| `-log-level` | `CM_LOG_LEVEL` | `info` | `debug` prints every file indexed |
| `-reset-db` | — | off | Delete the DB and re-index |
| `-version` | — | — | Print the build version |

Append `?theme=light` (or `dark`) to any URL to force a theme for screenshots.

Port **48273** was chosen because it is unassigned by IANA and not used by any
common dev tool (3000/5173/8080/8000/8888/9090 are all avoided). If it is busy the
app must exit with a clear error, not silently pick another port.

## Smoke check after a change

```bash
go vet ./... && go test ./... -count=1
go run ./cmd/claude-monitor -log-level debug &   # in background
curl -s http://127.0.0.1:48273/healthz            # expect {"status":"ok","indexed_sessions":N}
curl -s http://127.0.0.1:48273/api/v1/summary | head -c 400
```

Then open `http://127.0.0.1:48273/` in a browser (or the `claude-in-chrome` skill
for a screenshot) and confirm the dashboard renders numbers, not zeros.

## Running against a fixture instead of the real ~/.claude

`go run ./cmd/claude-monitor -claude-dir ./testdata/claude-home -db ./data/test.db`
— `testdata/claude-home` is a scrubbed miniature copy of the real layout and is
the safe way to develop UI without touching live data.
