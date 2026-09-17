# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`claude-monitor` — a local, single-binary Go web app that indexes the user's
`~/.claude` directory into SQLite and serves a dashboard (usage, projects,
sessions, settings, skills, plugins, plans, history, system info) on
**http://127.0.0.1:48273**. Read-only with respect to `~/.claude`; loopback only.

**Current status: planning phase.** `docs/PLAN.md` is the source of truth for
architecture, data model, routes and phased delivery. No application code exists
yet — build it phase by phase from that plan; do not invent a different layout.

## Commands

Toolchain on this machine: Go 1.24 **windows/386**, git, MSYS2 gcc (not needed —
CGO is disabled by design). No Node build step; UI assets are vendored + embedded.

```bash
go build -o bin/claude-monitor.exe ./cmd/claude-monitor   # build single binary
go run ./cmd/claude-monitor                               # dev run on :48273
go run ./cmd/claude-monitor -claude-dir ./testdata/claude-home -db ./data/test.db  # run on fixture
go vet ./... && gofmt -l .                                # lint (gofmt must print nothing)
go test ./... -count=1                                    # all tests (no -race on 386)
go test ./internal/claudedir/jsonl/... -run TestDedupe -v # single test
curl -s http://127.0.0.1:48273/healthz                    # smoke check
```

Config: flags `-addr -claude-dir -db -scan-interval -log-level` or env `CM_ADDR`,
`CM_CLAUDE_DIR`, `CM_DB`, `CM_SCAN_INTERVAL`, `CM_LOG_LEVEL` (flag > env > default).

## Architecture (see docs/PLAN.md §3–6 for detail)

Data flows one way: `internal/claudedir` (typed decoders for each `~/.claude`
file kind + deny-list) → `internal/indexer` (full scan on start, then fsnotify +
ticker; incremental by byte offset via `scan_state`; rebuilds rollup tables) →
`internal/store` (SQLite via `modernc.org/sqlite`, embedded SQL migrations, all
queries live here) → `internal/web` (stdlib mux, `html/template` pages, htmx
partials, `/api/v1` JSON) with `web/` embedded via `//go:embed`. The indexer is
the only writer; handlers only read.

Rules that are easy to get wrong (each has a reason in the skills below):

- **Dedupe assistant token usage by `message.id`** — Claude Code writes one line
  per streamed content block with the same `usage`; naive sums are ~3× too high.
- **Never read `~/.claude/.credentials.json` or `~/.claude/sessions/*.key`** —
  enforced in `.claude/settings.json` deny rules and must also be enforced in
  the indexer's walk.
- Real project path comes from transcript `cwd`, not from decoding the
  `projects/<encoded>` dir name (encoding is lossy).
- Pure-Go SQLite only (`modernc.org/sqlite`); never `mattn/go-sqlite3`.
- Port 48273, bind `127.0.0.1` only, exit with an error if the port is taken.
- Never render raw transcript/prompt text in the UI; titles, counts, names only.

## Project agents and skills

Skills (auto-loaded by description; load explicitly when relevant):
- `claude-dir-format` — verified layout + JSONL schema of `~/.claude` and parsing gotchas. **Read before any parser/schema work.**
- `go-sqlite-patterns` — driver, pragmas, migration layout, incremental-index transaction contract, test conventions.
- `run-app` — build/run/smoke-check recipe and config table.

Agents (`.claude/agents/`): `go-backend`, `frontend`, `db-engineer`, `reviewer`
(read-only checklist), `claude-dir-explorer` (read-only, inspects the live
`~/.claude` when a format question comes up). Use them per phase as PLAN.md §7
describes; run `reviewer` before declaring a phase done.

## Test data

`testdata/claude-home/` (to be created in Phase 1) is a scrubbed miniature of
the real `~/.claude`. Tests and UI development run against it — never against
the live directory. Include duplicated-usage lines and a malformed line on
purpose so the dedupe and tolerance paths are always exercised.
