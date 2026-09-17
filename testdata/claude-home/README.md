# Fixture: scrubbed miniature ~/.claude

Hand-built from real Claude Code 2.1.274 files with all text content replaced.
Deliberate features, relied on by the golden numbers in
internal/indexer/indexer_test.go:

- Session alpha/...0001: assistant `msg_A` and `msg_C` are written as several
  lines with identical `usage` (the real duplicate-line behaviour); one
  malformed line; one `attachment` line of an unknown shape; an `ai-title`.
- A subagent transcript (`agent-deadbeef`) with `isSidechain: true`.
- Session beta/...0002: `summary` line instead of `ai-title`, a `cwd` that does
  not exist (orphaned project), model claude-sonnet-5.
- `.credentials.json` and `sessions/*.key` contain sentinels that must never
  appear in any output.
- `history.jsonl` contains one non-JSON line.
