---
name: claude-dir-format
description: Reference for the on-disk layout and file formats of the user's ~/.claude directory (projects, session JSONL transcripts, stats-cache, history, settings, plugins, skills, plans). Load before writing or changing any indexer/parser code, any SQLite schema that mirrors ~/.claude, or when deciding what a page should display.
---

# ~/.claude directory format

Verified against Claude Code **2.1.274** on Windows (2026-09-17). Field names are
exact; treat anything not listed here as "unknown until inspected". When in doubt,
inspect a real file with `head -c`, `grep -o '"type":"[^"]*"' | sort | uniq -c`,
or the `claude-dir-explorer` agent — never guess a schema.

## Top level

| Path | Kind | What it is | Index it? |
|---|---|---|---|
| `settings.json` | JSON | User-scope settings (`permissions.defaultMode`, `model`, `theme`, `autoUpdatesChannel`, `autoContinueAtUsageLimit`, hooks, env, …) | Yes — snapshot + hash |
| `stats-cache.json` | JSON | Pre-aggregated usage stats (see below) | Yes — cheapest source of totals |
| `history.jsonl` | JSONL | One line per user prompt across all projects | Yes |
| `projects/` | dir | One sub-dir per project cwd; contains session transcripts | Yes — core data |
| `plans/<slug>.md` | md | Plan-mode documents (`# Title` first line) | Yes — list + first heading |
| `skills/synced/<id>/` | dir | Skills synced from claude.ai (each has `SKILL.md` with frontmatter) | Yes — name/description |
| `plugins/known_marketplaces.json` | JSON | Map of marketplace name → `{source:{source,repo}, installLocation, lastUpdated}` | Yes |
| `plugins/marketplaces/<name>/` | dir | Cloned marketplace repos | Count only |
| `plugins/synced/<id>/` | dir | Synced plugins | Count only |
| `file-history/<session-id>/` | dir | Snapshots of files edited per session (backs undo) | Size/count only |
| `backups/` | dir | Settings/config backups | Size/count only |
| `shell-snapshots/` | dir | Captured shell env per session | Size/count only |
| `paste-cache/` | dir | Cached pasted content | Size only |
| `cache/` | dir | Misc cache | Size only |
| `ide/` | dir | IDE integration lock files (often empty) | Count only |
| `downloads/` | dir | Downloaded files | Size only |
| `session-env/` | dir | Per-session env | Size only |
| `sessions/<pid>.json` | JSON | **Live** sessions: `{pid, sessionId, cwd, startedAt(ms), version, kind, entrypoint, name, …}` | Yes — "currently running" widget |
| `sessions/<pid>.<hash>.key` | secret | Peer token | **NEVER read** |
| `.credentials.json` | secret | OAuth/API credentials | **NEVER read, never list contents** |
| `.last-update-result.json` | JSON | `{timestamp, path, outcome, status, version_from, version_to, error_code}` | Yes — "installed version" |
| `.last-cleanup` | text | ISO timestamp of last auto-cleanup | Yes |

Security rule: the indexer must have an explicit **deny-list** (`.credentials.json`,
`sessions/*.key`) and must never render raw file contents of anything outside the
allow-listed JSON/JSONL/MD files above.

## `projects/<encoded-cwd>/`

Directory name is the project cwd with every `\`, `/`, `:` and space replaced by
`-` (e.g. `C--Users-manju-Desktop-Projects-Claude-Monitor`). This encoding is
**lossy** — do not try to decode it. Get the real path from the `cwd` field of any
`user`/`assistant` line inside a transcript, falling back to the encoded name.

Contents:

```
<session-uuid>.jsonl            # main transcript, one JSON object per line
<session-uuid>/subagents/       # optional: spawned subagents for that session
    agent-<id>.jsonl            #   subagent transcript (same line format)
    agent-<id>.meta.json        #   {agentType, description, toolUseId, spawnDepth, requestShape, requestNonInteractive}
memory/                         # optional: persistent memory files (MEMORY.md + *.md with YAML frontmatter)
```

## Session transcript JSONL — line types that matter

Every line has `"type"`. Only a handful are needed for monitoring:

| `type` | Key fields | Use |
|---|---|---|
| `user` | `uuid`, `parentUuid`, `timestamp` (ISO), `cwd`, `sessionId`, `version`, `message.role`, `message.content` (string **or** array of blocks), `isSidechain`, `entrypoint` | message count, cwd, CLI version, first/last timestamp |
| `assistant` | same envelope + `message.id` (`msg_…`), `message.model`, `message.usage`, `message.content[]` blocks of type `text` / `thinking` / `tool_use` | tokens per model, tool-call counts |
| `ai-title` | `aiTitle`, `sessionId` | session display title |
| `mode` / `permission-mode` | `mode` | informational |
| `summary` | `summary`, `leafUuid` | older sessions may carry a summary line |
| `system` | misc | ignore |
| everything else (`attachment`, `prompt_snapshot`, `file-history-snapshot`, `deferred_tools_*`, `total_tokens_reminder`, …) | — | ignore, but tolerate unknown types |

`message.usage` shape:

```json
{"input_tokens":2,"cache_creation_input_tokens":11445,"cache_read_input_tokens":32796,
 "output_tokens":323,"output_tokens_details":{"thinking_tokens":120}}
```

`tool_use` content block: `{"type":"tool_use","id":"toolu_…","name":"Bash","input":{…}}`.
`tool_result` blocks live inside **user** lines' `message.content[]` — pairing is by
`tool_use_id`.

### Parsing gotchas (these WILL corrupt numbers if ignored)

1. **Duplicate assistant lines.** One API response is written as several lines
   (one per streamed content block) that share the same `message.id` and carry the
   *identical* `usage` object. **Dedupe token usage by `message.id`** — count usage
   once per id, but aggregate `tool_use` blocks across all lines with that id.
2. `message.content` on `user` lines is a plain string for typed prompts but an
   array of blocks for tool results / attachments. Handle both.
3. Lines can be very large (hundreds of KB — pasted content, tool output). Use
   `bufio.Scanner` with a big buffer (≥ 10 MB) or `bufio.Reader.ReadBytes('\n')`.
4. Files are appended live while a session runs. Index **incrementally**: remember
   `(path, size, mtime, byte_offset)` and resume from the offset; re-scan from 0 if
   the file shrank (rotated/rewritten).
5. `isSidechain: true` lines belong to subagent/fork activity; keep the flag so the
   UI can separate main-thread vs subagent usage.
6. Timestamps are UTC ISO-8601 with millis. Session start = min `timestamp`,
   end = max `timestamp`; duration = diff.
7. Windows paths in `cwd` are backslash-escaped in JSON (`C:\\Users\\…`).

## `stats-cache.json` (v5)

```json
{"version":5,"lastComputedDate":"2026-09-14",
 "dailyActivity":[{"date":"2026-09-12","messageCount":1390,"sessionCount":11,"toolCallCount":294}],
 "dailyModelTokens":[{"date":"2026-09-12","tokensByModel":{"claude-opus-5":48935913}}],
 "modelUsage":{"claude-opus-5":{"inputTokens":…,"outputTokens":…,"cacheReadInputTokens":…,
               "cacheCreationInputTokens":…,"webSearchRequests":0,"costUSD":0}},
 "totalSessions":24,"totalMessages":8580,
 "longestSession":{"sessionId":"…","duration":183260549,"messageCount":697,"timestamp":"…"},
 "firstSessionDate":"2026-08-15T00:47:03.665Z",
 "hourCounts":{"0":2,"22":6}}
```

It is computed lazily by Claude Code and can lag by days (`lastComputedDate`).
Show it as "Claude's own stats" and show our transcript-derived numbers alongside —
they will not match exactly and that is expected. `costUSD` is 0 for subscription
users; do not invent prices.

## `history.jsonl`

`{"display":"<prompt text>","pastedContents":{…},"project":"<absolute cwd>","sessionId":"…","timestamp":<ms epoch>}`.
`project` here is the **real** path (not encoded) — a good join key to
`projects/`. Truncate `display` for lists; it can be huge.

## Settings resolution (for the Settings page)

Scopes, lowest → highest precedence:
1. `~/.claude/settings.json` (user)
2. `<project>/.claude/settings.json` (project, committed)
3. `<project>/.claude/settings.local.json` (project, local)

Also relevant per project: `<project>/CLAUDE.md`, `<project>/.claude/agents/*.md`,
`<project>/.claude/skills/*/SKILL.md`, `<project>/.mcp.json`. Project files live at
the real cwd (from transcripts), not under `~/.claude` — read them only if the
directory still exists, and mark projects whose cwd is gone as *orphaned*.
