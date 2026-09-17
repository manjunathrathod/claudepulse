# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`claude-monitor` — a local, single-binary Go web app that indexes the user's
`~/.claude` directory into SQLite and serves a dashboard (usage, projects,
sessions, settings, skills, plugins, plans, history, system info) on
**http://127.0.0.1:48273**. Read-only with respect to `~/.claude`; loopback only.

**Current status: v1 complete — all six phases in `docs/PLAN.md` §7 are done.**
Further work should be small, reviewed changes; keep the phase discipline
(implement → `reviewer` agent → screenshot for UI → commit). `docs/PLAN.md` is the source of
truth for architecture, data model, routes, the UI design system and phased
delivery — build phase by phase from it; do not invent a different layout.
`docs/PLAN.md` §7 tracks which phases are complete.

## Commands

Toolchain on this machine: Go **windows/386** (32-bit). `go.mod` pins Go 1.25
because `modernc.org/sqlite` needs it; `GOTOOLCHAIN=auto` downloads it on first
build. MSYS2 gcc is present but unused — CGO is disabled by design. No Node
build step; UI assets are vendored + embedded. `jq` is not installed (use `node -e`
for ad-hoc JSON).

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/claude-monitor.exe ./cmd/claude-monitor   # build; version shows on /system
go run ./cmd/claude-monitor                               # dev run on :48273
go run ./cmd/claude-monitor -claude-dir ./testdata/claude-home -db ./data/test.db  # run on fixture
go vet ./... && gofmt -l .                                # lint (gofmt must print nothing)
go test ./... -count=1                                    # all tests (no -race on 386)
go test ./internal/indexer/ -run TestGoldenNumbers -v      # single test (the dedupe guard)
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
partials, `/api/v1` JSON). UI assets live in `web/` and are embedded by the
tiny `webassets` package (`web/assets.go`) because `//go:embed` cannot reach
outside a package directory. The indexer is the only writer; handlers only read.

UI conventions: one template set per page (`base.html` + `partials/*` + page),
view models built in `internal/web/pages.go`, formatting via template funcs in
`templates.go` (`compact`, `comma`, `duration`, `ago`, `pctNum`…). Chart data is
emitted as `<script type="application/json">` and read by `web/static/app.js`;
never compute numbers in JS. Model→colour mapping is fixed and validated — see
PLAN.md "UI design system" before touching it. Verify visually with headless
Edge screenshots (command in PLAN.md).

Rules that are easy to get wrong (each has a reason in the skills below):

- **Dedupe assistant token usage by `message.id`** — Claude Code writes one line
  per streamed content block with the same `usage`; naive sums are ~3× too high.
- **Never read `~/.claude/.credentials.json` or `~/.claude/sessions/*.key`** —
  enforced in `.claude/settings.json` deny rules and must also be enforced in
  the indexer's walk.
- `~/.claude.json` (sibling of the `.claude` dir, outside it) is the ONLY file
  read from outside `-claude-dir`; `claudedir.ReadAccount` decodes just the
  account profile and cached usage fields. It holds no tokens, but never store
  or render it raw.
- Real project path comes from transcript `cwd`, not from decoding the
  `projects/<encoded>` dir name (encoding is lossy).
- Pure-Go SQLite only (`modernc.org/sqlite`); never `mattn/go-sqlite3`.
- Port 48273, bind `127.0.0.1` only, exit with an error if the port is taken.
- Never render raw transcript/prompt text in the UI; titles, counts, names only.
  One sanctioned exception: the Plans & history page lists the first 160 runes
  of each prompt from `history.jsonl` (`historyDisplayRunes`), because that is
  what the page is for. Nothing else may show message content.

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

`testdata/claude-home/` is a scrubbed miniature of the real `~/.claude` (see its
README for what each quirk exercises). Tests and UI development run against it —
never against the live directory. `internal/indexer/indexer_test.go` pins
**golden numbers** derived from it (25 input / 270 output / 7200 cache-read
tokens, 5 assistant messages, 4 tool calls…). If a parser change breaks them,
re-check the dedupe rules in the `claude-dir-format` skill before editing the
expected values. JSON fixture files containing `\\` must be written with the
Write tool, not bash heredocs (Git Bash collapses the escapes).
