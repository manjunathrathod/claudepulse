# claude-monitor — Implementation Plan

Status: **planning — no application code written yet.** Last updated 2026-09-17.

## 1. Goal

A local, single-binary web app that watches the user's `~/.claude` directory and
presents: usage (tokens, messages, tool calls, per model / per day / per hour),
the list of projects and their sessions, effective settings, skills, plugins,
plans, prompt history, live sessions, and housekeeping info (CLI version, last
update, last cleanup, disk usage per sub-directory). Data is indexed into SQLite
so pages stay fast as `projects/` grows (already 157 MB / 24 sessions here).

Non-goals (v1): editing anything under `~/.claude`, multi-user, remote access,
cost estimation in USD (subscription users have `costUSD: 0`; no invented prices).

## 2. Findings from the real `~/.claude` (drives every decision below)

Full reference: `.claude/skills/claude-dir-format/SKILL.md`. Highlights:

- `projects/<encoded-cwd>/<session>.jsonl` is the core data. Encoding is lossy;
  the real cwd is in each line's `cwd` field.
- Assistant usage is **repeated across several lines per API message** (same
  `message.id`, identical `usage`). Must dedupe or totals are ~3× too high.
- Files are appended live; index incrementally by byte offset.
- `stats-cache.json` is a ready-made aggregate but lags (`lastComputedDate`).
- `history.jsonl` has the real project path → clean join key.
- `sessions/<pid>.json` shows currently running Claude Code processes.
- `.credentials.json` and `sessions/*.key` are secrets → hard deny-list.
- Toolchain is **Go 1.24 windows/386** → pure-Go SQLite, no CGO.

## 3. Architecture

```
┌──────────────────────────── claude-monitor.exe ────────────────────────────┐
│                                                                            │
│  internal/claudedir      internal/indexer          internal/store          │
│  (walk + parse files) ─► (incremental sync, ───►  (SQLite, migrations,     │
│   deny-list, JSONL       fsnotify + ticker,        queries, rollups)       │
│   decoders)              dedupe, rollups)               ▲                  │
│                                                         │ read-only         │
│  internal/web  ◄────────────────────────────────────────┘                  │
│  (net/http mux, html/template pages, htmx partials, /api/v1 JSON)          │
│  web/ (templates, app.css, vendored htmx + Chart.js) — //go:embed          │
└────────────────────────────────────────────────────────────────────────────┘
        ▲ reads only                                   ▲ http://127.0.0.1:48273
   %USERPROFILE%\.claude                              browser
```

Package layout:

```
cmd/claude-monitor/main.go      flags/env → config → store.Open → indexer.Start → web.Serve
internal/config                 Config struct, defaults, flag+env parsing
internal/claudedir              Path helpers, deny-list, typed decoders for every file kind
internal/claudedir/jsonl        Streaming transcript reader (big-buffer scanner, tolerant)
internal/indexer                Scanner (full + incremental), watcher, rollup jobs
internal/store                  SQLite open/migrate, queries_*.go, migrations/*.sql
internal/web                    Router, handlers, view models, template funcs
web/templates, web/static       UI assets (embedded)
testdata/claude-home            Scrubbed miniature ~/.claude fixture
docs/                           This plan + ADRs
```

### Stack decisions

| Concern | Choice | Why |
|---|---|---|
| Language | Go 1.24, stdlib `net/http` (1.22+ pattern routing) | No framework needed for ~15 routes |
| DB | SQLite via `modernc.org/sqlite`, WAL mode | Pure Go; works on windows/386; single file |
| File watching | `github.com/fsnotify/fsnotify` + 30 s full-rescan ticker | fsnotify is fast path; ticker is the safety net (Windows can drop events) |
| UI | `html/template` + **htmx** + **Chart.js**, vendored, embedded | No Node build step; one binary; matches "local tool" scope |
| Styling | Single `app.css`, CSS variables, dark-first with light override | User's Claude theme is dark |
| Logging | `log/slog` | stdlib |
| Port | **48273**, loopback only | Unassigned by IANA, unused by common dev tools; fail loudly if busy |
| Config | flags > env (`CM_*`) > defaults | See `run-app` skill |

Only two external modules: `modernc.org/sqlite`, `github.com/fsnotify/fsnotify`.

## 4. Data model (SQLite)

```sql
schema_migrations(version PK, applied_at)
scan_state(path PK, size, mtime, byte_offset, last_scanned_at)      -- incremental cursor

projects(id PK, encoded_name UNIQUE, real_path, exists_on_disk, has_claude_md,
         has_project_settings, memory_file_count, first_seen, last_activity, source_path)
sessions(id PK /*uuid*/, project_id FK, title, cwd, cli_version, entrypoint,
         started_at, ended_at, user_msg_count, assistant_msg_count, tool_call_count,
         subagent_count, source_path)
messages(id PK /*msg_… or line uuid*/, session_id FK, role, model, ts, is_sidechain,
         input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, thinking_tokens)
tool_calls(id PK /*toolu_…*/, session_id FK, message_id FK, name, ts)
subagents(id PK, session_id FK, agent_type, description, spawn_depth, msg_count, source_path)

-- materialised rollups (rebuilt by indexer after each sync batch)
daily_usage(date, model, msgs, tool_calls, input_tokens, output_tokens,
            cache_read_tokens, cache_create_tokens, PK(date, model))
session_usage(session_id PK, model_json, total_tokens, duration_ms)
hour_counts(hour PK, count)

-- other file kinds
history_entries(id PK, ts, project_path, session_id, display_trunc, source_path)
settings_snapshots(id PK, scope /*user|project|local*/, project_id NULL, path,
                   sha256, json, captured_at)
plans(id PK, slug, title, mtime, size, source_path)
skills(id PK, origin /*synced|project*/, name, description, path)
plugins(id PK, marketplace, name, install_location, last_updated)
live_sessions(pid PK, session_id, cwd, started_at, version, name, seen_at)
dir_stats(name PK, file_count, bytes, computed_at)
meta(key PK, value)     -- cli_version, last_update_json, last_cleanup, stats_cache_json
```

Indexes: `messages(session_id, ts)`, `messages(ts)`, `tool_calls(session_id, name)`,
`sessions(project_id, started_at)`, `history_entries(ts)`.

## 5. Indexer behaviour

1. **Startup full scan** (in background; UI serves whatever is indexed so far,
   `/healthz` reports progress).
2. Walk `projects/**.jsonl` (main + `subagents/*.jsonl`). For each file consult
   `scan_state`; parse only new bytes; upsert sessions/messages/tool_calls in one
   transaction per file; update `scan_state` in the same transaction.
3. Parse single-file sources (`settings.json`, `stats-cache.json`, `history.jsonl`,
   `.last-update-result.json`, `.last-cleanup`, `plugins/known_marketplaces.json`,
   `sessions/*.json`, `plans/*.md`, `skills/**/SKILL.md`) only when `sha256`/mtime
   changed.
4. For each project with an existing `real_path`, read `<real_path>/CLAUDE.md`,
   `.claude/settings.json`, `.claude/settings.local.json`, `.claude/agents`,
   `.claude/skills` (metadata only). Mark orphaned projects.
5. `du`-style size per top-level sub-directory → `dir_stats` (throttled: every 5 min).
6. After any batch: rebuild `daily_usage`, `session_usage`, `hour_counts` for the
   affected sessions' dates (not the whole table).
7. fsnotify on `projects/` (recursive: re-add watches for new project dirs),
   `sessions/`, `history.jsonl`, `settings.json`; debounce 500 ms; ticker rescan
   every `-scan-interval` as fallback.
8. Never read deny-listed paths; never write under `~/.claude`.

## 6. HTTP surface

Pages (server-rendered):

| Route | Content |
|---|---|
| `/` | Dashboard: stat tiles (projects, sessions, messages, tool calls, tokens by model), daily activity chart (30/90 d), hour-of-day bars, live sessions, "Claude's own stats" card from `stats-cache.json` with `lastComputedDate` |
| `/projects` | Table: name, real path, exists?, sessions, last activity, tokens |
| `/projects/{id}` | Sessions list, memory files, CLAUDE.md/settings presence, per-project usage |
| `/sessions` | All sessions, filter by project/model/date |
| `/sessions/{id}` | Title, duration, model mix, tool-call breakdown, subagents, timeline (message counts per 10 min) |
| `/settings` | User settings.json (pretty, key-by-key), per-project overrides, diff vs user |
| `/skills` | Synced + per-project skills with descriptions |
| `/plugins` | Marketplaces + synced plugins |
| `/plans` | Plan files with title + mtime |
| `/history` | Prompt history (truncated), searchable, links to session |
| `/system` | CLI version, last update result, last cleanup, dir sizes, index status |

JSON API (`/api/v1/...`) mirrors the above for charts/htmx partials: `summary`,
`daily?days=`, `projects`, `sessions?project=`, `sessions/{id}`, `live`, `system`.
`/healthz` → `{"status":"ok","indexed_sessions":N,"scanning":bool}`.

## 7. Phased delivery

| Phase | Deliverable | Done when |
|---|---|---|
| **0** (this) | Repo init, agents, skills, plan | ✅ |
| **1** Core index | `config`, `claudedir` decoders, `jsonl` reader, `store` + migrations, `indexer` full scan, `/healthz`, `/api/v1/summary` | `go test ./...` green on fixtures; summary numbers match a hand count of one session |
| **2** Dashboard + projects | `base.html`, `/`, `/projects`, `/projects/{id}`, Chart.js daily + model charts | Renders real data in browser at :48273 |
| **3** Sessions | `/sessions`, `/sessions/{id}`, tool-call & subagent breakdown | Longest session (697 msgs) page loads < 200 ms |
| **4** Settings / skills / plugins / plans / history / system pages | All remaining routes | Every top-level `~/.claude` item is represented somewhere |
| **5** Live | fsnotify watcher, incremental resume, live-sessions widget polling via htmx | Start a new Claude session → appears on dashboard within 10 s without restart |
| **6** Polish | light theme, empty states, `-reset-db`, README, `reviewer` pass | Reviewer checklist clean |

Each phase: implement with the matching agent (`go-backend`, `db-engineer`,
`frontend`), then run `reviewer`. Commit per phase.

## 8. Testing strategy

- `testdata/claude-home/` — a scrubbed copy of the real layout: 2 projects, 3
  sessions (one with subagents), duplicated-usage lines included on purpose,
  one malformed line, one 1 MB line. Built once by a small script; text content
  replaced with `"…"`.
- Parser tests are table-driven; store tests use in-memory SQLite; handler tests
  use `httptest` against a store seeded from the fixture.
- Golden-number test: fixture session X must yield exactly N tokens (computed by
  hand once) — guards the dedupe rule forever.

## 9. Open questions (decide before Phase 1)

1. Retention: index sessions Claude Code has already auto-cleaned? (Default: no —
   if the file is gone, prune the rows; `cleanupPeriodDays` is 30.)
2. Should project detail read the project's own `.claude/` on every request or
   only at index time? (Default: index time, refreshed by ticker.)
3. Windows service / tray auto-start? (Default: no; document a Task Scheduler
   one-liner in README.)
