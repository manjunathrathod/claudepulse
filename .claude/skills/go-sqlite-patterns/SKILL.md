---
name: go-sqlite-patterns
description: Project conventions for Go + SQLite in claude-monitor - driver choice (pure-Go modernc.org/sqlite, no CGO), connection pragmas, embedded SQL migrations, query layout, and incremental-indexing transactions. Load before touching internal/store, internal/db, or any migration file.
---

# Go + SQLite conventions for claude-monitor

## Driver: `modernc.org/sqlite` — never `mattn/go-sqlite3`

The local toolchain is **Go 1.24 windows/386** with an MSYS2 gcc. CGO on 32-bit
Windows is fragile and `mattn/go-sqlite3` would force every contributor to have a
matching C toolchain. `modernc.org/sqlite` is a pure-Go transpile of SQLite,
supports `windows/386`, and gives a single static binary.

```go
import (
    "database/sql"
    _ "modernc.org/sqlite"
)
db, err := sql.Open("sqlite", dsn)
```

DSN: `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)`.
Call `db.SetMaxOpenConns(1)` for the **writer** pool; open a second `*sql.DB` with
a larger pool for read-only HTTP handlers if contention shows up. WAL lets readers
proceed while the indexer writes.

## Layout

```
internal/store/
  store.go        // Open(), Close(), migrate on open
  migrations/     // 0001_init.sql, 0002_….sql  — embedded with //go:embed
  queries_*.go    // one file per domain: projects, sessions, usage, settings…
```

- Migrations are plain SQL files applied in filename order inside one transaction
  each, tracked in `schema_migrations(version INTEGER PRIMARY KEY, applied_at)`.
  Never edit a migration that has shipped; add a new one.
- No ORM, no sqlc for now. Hand-written SQL with `database/sql`; scan into plain
  structs. Keep every query in `internal/store` — handlers never see SQL.
- All timestamps stored as **UTC RFC3339 text** (`2026-09-17T09:58:26.091Z`) so
  SQLite date functions and Go `time.Parse` both work; ms-epoch inputs are
  converted on write.
- Every table that mirrors a file gets `source_path TEXT` so the UI can link back
  and the indexer can prune rows whose file disappeared.

## Incremental indexing contract

`scan_state(path PRIMARY KEY, size, mtime, byte_offset, last_scanned_at)`.

For each JSONL: if `size < stored size` → truncate rows for that path and restart
at 0; else resume at `byte_offset`. Wrap **one file's** batch of inserts in one
transaction and update `scan_state` in the same transaction — a crash must never
leave rows without a matching offset. Use `INSERT … ON CONFLICT DO UPDATE` keyed on
natural ids (`session_id`, `message.id`) so re-scans are idempotent.

## Testing

- Unit-test parsers against small fixture files in `testdata/` copied from real
  transcripts **with content scrubbed** (keep envelope + usage, blank out text).
- Store tests open `file::memory:?cache=shared` — no temp files.
- `go test ./... -race` is not available on 386; run plain `go test ./...` and
  rely on `-count=1` for freshness.
