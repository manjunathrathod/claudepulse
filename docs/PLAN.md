# claude-monitor — Implementation Plan

Status: **All six phases complete (v1).** Ideas for later are in §10.
Last updated 2026-09-17.

### UI design system (Phase 2, fixed)

Brief: dark, glassmorphism cards, blue + purple accents, minimal, desktop-first.
- Tokens live in `web/static/app.css :root` (dark) with a `[data-theme="light"]`
  override. UI accents: `--accent #4f8ff7` (blue), `--accent-2 #a78bfa` (purple).
- **Chart series are not the UI accents.** Blue↔violet fails CVD separation
  (ΔE 1.9 protan), so data uses the validated categorical set
  blue `#3987e5` / magenta `#d55181` / amber `#c98500` / gray `#6b7186` for
  opus / sonnet / haiku / other — mapped by model *name* in
  `internal/web/templates.go` so colour follows the entity. Re-run the dataviz
  validator (`--pairs all --mode dark --surface #0f1117`) before changing it.
- Charts: Chart.js only for the stacked daily bar (thin bars ≤22px, 4px rounded
  ends, 2px surface gap, hairline grid, legend only for ≥2 series). Everything
  else (model share, tools, disk, hour-of-day) is CSS meters — no JS needed.
- Fonts: Inter + JetBrains Mono via Google Fonts (falls back to system fonts
  offline). htmx polls `/partials/live` (10 s) and `/partials/index` (15 s).
- Hover help: every KPI label, card title and nav item carries a one-line
  description via `{{template "help.html" "…"}}` (a `?` badge with `title`) or a
  `title` attribute. Keep them to one sentence, no jargon.
- Visual verification without the Chrome extension: headless Edge
  `msedge --headless=new --screenshot=… --window-size=1440,1750 http://127.0.0.1:48273/`.

Verified during Phase 1: Claude's own `stats-cache.json` sums usage per transcript
line (no dedupe), so its token figures run ~2–3× above the deduped API accounting
this app computes. The UI must label the two sources rather than reconcile them.

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
`daily?days=`, `sessions?project=…`, `sessions/{id}`, `live`, `system` (project,
settings, skills, plans and history are HTML-only in v1).
`/healthz` → `{"status":"ok","indexed_sessions":N,"scanning":bool}`.

## 7. Phased delivery

| Phase | Deliverable | Done when |
|---|---|---|
| **0** | Repo init, agents, skills, plan | ✅ 2026-09-17 |
| **1** Core index | `config`, `claudedir` decoders, `jsonl` reader, `store` + migrations, `indexer` full + incremental scan, `/healthz`, `/api/v1/{summary,daily,live,system}` | ✅ 2026-09-17 — golden-number tests on fixture; real `~/.claude` (157 MB, 50 files) indexes in 1.5 s |
| **2** Dashboard + projects | `base.html`, `/`, `/projects`, `/projects/{id}`, Chart.js daily chart, CSS meters, htmx live/index partials | ✅ 2026-09-17 — verified via headless-Edge screenshots against real data |
| **3** Sessions | `/sessions` (project/model/range/title filters, pagination), `/sessions/{id}` (hero stats, adaptive-bucket timeline, subagents, models, tools), `/api/v1/sessions[/{id}]` | ✅ 2026-09-17 — 506-message session renders in ~30 ms |
| **4** Settings / skills / plugins / plans / history / system pages | `/settings` (user + per-project, redacted), `/skills` (skills, marketplaces, installed/synced plugins), `/plans` (plans + searchable prompt history), `/system`; hover help on every component | ✅ 2026-09-17 — every top-level `~/.claude` item is represented |
| **5** Live + account | fsnotify watcher (debounced, re-synced after every scan), account name + plan + rate-limit usage from `~/.claude.json` in sidebar, dashboard and System | ✅ 2026-09-17 — new session indexed ~3 s after its file appears; pruned ~3 s after deletion |
| **6** Polish | dark/light toggle (persisted, charts re-render), version stamping (`-version`, `-ldflags -X main.version`), empty-directory smoke test, README + LICENSE, final reviewer pass | ✅ 2026-09-17 |

Each phase: implement with the matching agent (`go-backend`, `db-engineer`,
`frontend`), then run `reviewer`. Commit per phase.

## 8. Testing strategy

- `testdata/claude-home/` — a hand-built scrubbed copy of the real layout: 2
  projects, 2 sessions (one with a subagent), duplicated-usage lines included on
  purpose, one malformed line, secret sentinels. Oversized lines (3 MB) are
  covered in-memory by `jsonl.TestScanHandlesHugeLine` rather than on disk.
- Parser tests are table-driven; store tests use in-memory SQLite; handler tests
  use `httptest` against a store seeded from the fixture.
- Golden-number test: fixture session X must yield exactly N tokens (computed by
  hand once) — guards the dedupe rule forever.

## 9. Decisions and known limitations

Decided 2026-09-17 (defaults accepted):
1. Retention: rows are pruned when Claude Code auto-cleans a transcript
   (`cleanupPeriodDays`, 30). Pruning is skipped when `projects/` lists empty.
2. Project `.claude/` metadata is read at index time, refreshed by the ticker.
3. No service/tray auto-start; README will document a Task Scheduler one-liner.

Known limitations carried from the Phase 1 review (revisit in Phase 5/6):
- `messages.id` is a global PK. If Claude Code ever copies history into a second
  transcript with the same `message.id`s, the copy is ignored and pruning the
  original would drop them. Verify with `claude-dir-explorer` whether
  fork/resume produces such duplicates; a `(id, session_id)` PK would remove the risk.
- Incremental scan resumes at the stored byte offset whenever a file grew or its
  mtime changed. A same-size or larger *rewrite* (not append) is therefore
  indexed incorrectly until `-reset-db`. Claude Code only appends, so this is
  accepted for now; a cheap guard would be to re-verify the last N bytes.
- `-db` paths containing `?`, `#` or `%` are not URI-escaped in the DSN.
- Two `projects` rows that resolve to the same real path (Windows case variants
  of a cwd) share one `settings_snapshots`/`skills` row; the last one scanned wins.
- `MarketplacePluginCount` reports 0 for marketplaces installed outside
  `~/.claude` (the deny-list refuses paths outside the root).

## 10. Ideas for later (not planned)

- Cost estimates for API-key users (needs a price table; subscription users
  have `costUSD: 0`).
- A "compare two periods" view on the Overview.
- Export a session's stats as CSV/JSON from the session page.
- Optional desktop notification when a rate-limit window crosses 80 %.
- Linux/macOS service files (the code is portable; only the docs are Windows-first).
