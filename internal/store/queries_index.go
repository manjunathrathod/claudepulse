package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ScanState is the incremental cursor for one file.
type ScanState struct {
	Path       string
	Size       int64
	MTime      time.Time
	ByteOffset int64
}

// GetScanState returns the cursor for path, or ok=false if never scanned.
func (s *Store) GetScanState(ctx context.Context, path string) (ScanState, bool, error) {
	var st ScanState
	var mtime string
	err := s.db.QueryRowContext(ctx, `SELECT path, size, mtime, byte_offset FROM scan_state WHERE path = ?`, path).
		Scan(&st.Path, &st.Size, &mtime, &st.ByteOffset)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	st.MTime, _ = time.Parse(time.RFC3339Nano, mtime)
	return st, true, nil
}

// PutScanState upserts the cursor inside tx.
func PutScanState(ctx context.Context, tx *sql.Tx, st ScanState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO scan_state(path, size, mtime, byte_offset, last_scanned_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size, mtime=excluded.mtime,
			byte_offset=excluded.byte_offset, last_scanned_at=excluded.last_scanned_at`,
		st.Path, st.Size, FormatTime(st.MTime), st.ByteOffset, now())
	return err
}

// DeleteFileRows removes every row derived from sourcePath (used when a file
// shrank/rotated or disappeared) and its scan cursor.
func DeleteFileRows(ctx context.Context, tx *sql.Tx, sourcePath string) error {
	for _, q := range []string{
		`DELETE FROM tool_calls WHERE source_path = ?`,
		`DELETE FROM messages WHERE source_path = ?`,
		`DELETE FROM subagents WHERE source_path = ?`,
		`DELETE FROM scan_state WHERE path = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, sourcePath); err != nil {
			return err
		}
	}
	return nil
}

// UpsertProject inserts or refreshes a project and returns its id.
func (s *Store) UpsertProject(ctx context.Context, encodedName, sourcePath string) (int64, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(encoded_name, source_path, first_seen)
			VALUES (?, ?, ?) ON CONFLICT(encoded_name) DO UPDATE SET source_path = excluded.source_path`,
			encodedName, sourcePath, now()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE encoded_name = ?`, encodedName).Scan(&id)
	})
	return id, err
}

// ProjectRealPath returns the stored real path for a project ("" if unknown).
func (s *Store) ProjectRealPath(ctx context.Context, id int64) (string, error) {
	var p sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT real_path FROM projects WHERE id = ?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p.String, err
}

// ProjectMeta is the per-project info refreshed by the indexer.
type ProjectMeta struct {
	RealPath           string
	ExistsOnDisk       bool
	HasClaudeMD        bool
	HasProjectSettings bool
	MemoryFileCount    int
}

// UpdateProjectMeta stores derived project info.
func (s *Store) UpdateProjectMeta(ctx context.Context, id int64, m ProjectMeta) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET real_path = COALESCE(?, real_path), exists_on_disk = ?,
		has_claude_md = ?, has_project_settings = ?, memory_file_count = ?,
		last_activity = (SELECT MAX(ended_at) FROM sessions WHERE project_id = projects.id)
		WHERE id = ?`,
		nullStr(m.RealPath), b2i(m.ExistsOnDisk), b2i(m.HasClaudeMD), b2i(m.HasProjectSettings), m.MemoryFileCount, id)
	return err
}

// EnsureSession creates the session row if missing (inside tx).
func EnsureSession(ctx context.Context, tx *sql.Tx, id string, projectID int64, sourcePath string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id, project_id, source_path) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id = excluded.project_id, source_path = excluded.source_path`,
		id, projectID, sourcePath)
	return err
}

// MessageRow is one deduped message.
type MessageRow struct {
	ID, SessionID, AgentID, Role, Kind, Model, SourcePath string
	TS                                                    time.Time
	IsSidechain                                           bool
	Input, Output, CacheRead, CacheCreate, Thinking       int64
}

// InsertMessage inserts a message, ignoring duplicates (the same message.id
// appears on several transcript lines — this is the dedupe point).
func InsertMessage(ctx context.Context, tx *sql.Tx, m MessageRow) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO messages(id, session_id, agent_id, role, kind, model, ts,
		is_sidechain, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, thinking_tokens, source_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, nullStr(m.AgentID), m.Role, m.Kind, nullStr(m.Model), FormatTime(m.TS),
		b2i(m.IsSidechain), m.Input, m.Output, m.CacheRead, m.CacheCreate, m.Thinking, m.SourcePath)
	return err
}

// ToolCallRow is one tool_use block.
type ToolCallRow struct {
	ID, SessionID, MessageID, AgentID, Name, SourcePath string
	TS                                                  time.Time
}

// InsertToolCall inserts a tool call, ignoring duplicates by toolu id.
func InsertToolCall(ctx context.Context, tx *sql.Tx, t ToolCallRow) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tool_calls(id, session_id, message_id, agent_id, name, ts, source_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.MessageID, nullStr(t.AgentID), t.Name, FormatTime(t.TS), t.SourcePath)
	return err
}

// SessionFacts are envelope facts learned while scanning a transcript. Empty
// fields mean "not seen in this batch" and are not overwritten.
type SessionFacts struct {
	Title, CWD, CLIVersion, Entrypoint string
}

// UpdateSessionFacts applies facts and recomputes counts/timestamps from the
// messages table, so it is idempotent across incremental batches.
func UpdateSessionFacts(ctx context.Context, tx *sql.Tx, id string, f SessionFacts) error {
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET
		title       = COALESCE(?, title),
		cwd         = COALESCE(?, cwd),
		cli_version = COALESCE(?, cli_version),
		entrypoint  = COALESCE(?, entrypoint),
		started_at  = (SELECT MIN(ts) FROM messages WHERE session_id = sessions.id AND ts <> ''),
		ended_at    = (SELECT MAX(ts) FROM messages WHERE session_id = sessions.id AND ts <> ''),
		user_msg_count      = (SELECT COUNT(*) FROM messages WHERE session_id = sessions.id AND role = 'user' AND kind = 'prompt' AND agent_id IS NULL),
		assistant_msg_count = (SELECT COUNT(*) FROM messages WHERE session_id = sessions.id AND role = 'assistant'),
		tool_call_count     = (SELECT COUNT(*) FROM tool_calls WHERE session_id = sessions.id),
		subagent_count      = (SELECT COUNT(*) FROM subagents WHERE session_id = sessions.id)
		WHERE id = ?`,
		nullStr(f.Title), nullStr(f.CWD), nullStr(f.CLIVersion), nullStr(f.Entrypoint), id)
	return err
}

// SubagentRow mirrors agent-<id>.meta.json plus counts. HasMeta is false when
// the .meta.json was absent, in which case existing metadata is kept.
type SubagentRow struct {
	ID, SessionID, AgentType, Description, SourcePath string
	SpawnDepth                                        int
	HasMeta                                           bool
}

// UpsertSubagent records a subagent and refreshes its message count.
func UpsertSubagent(ctx context.Context, tx *sql.Tx, r SubagentRow) error {
	var depth any
	if r.HasMeta {
		depth = r.SpawnDepth
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO subagents(id, session_id, agent_type, description, spawn_depth, msg_count, source_path)
		VALUES (?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(id) DO UPDATE SET agent_type = COALESCE(excluded.agent_type, agent_type),
			description = COALESCE(excluded.description, description),
			spawn_depth = COALESCE(excluded.spawn_depth, spawn_depth),
			source_path = excluded.source_path`,
		r.ID, r.SessionID, nullStr(r.AgentType), nullStr(r.Description), depth, r.SourcePath)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE subagents SET msg_count =
		(SELECT COUNT(*) FROM messages WHERE agent_id = subagents.id AND session_id = subagents.session_id) WHERE id = ?`, r.ID); err != nil {
		return err
	}
	// Subagents are discovered after the main transcript's facts were written.
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET subagent_count =
		(SELECT COUNT(*) FROM subagents WHERE session_id = sessions.id) WHERE id = ?`, r.SessionID)
	return err
}

// PruneMissing deletes rows whose source file is no longer in keep (a set of
// absolute transcript paths) — Claude Code auto-cleans old transcripts.
func (s *Store) PruneMissing(ctx context.Context, keep map[string]bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM scan_state`)
	if err != nil {
		return 0, err
	}
	var gone []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[p] {
			gone = append(gone, p)
		}
	}
	rows.Close()
	if len(gone) == 0 {
		return 0, nil
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		for _, p := range gone {
			if err := DeleteFileRows(ctx, tx, p); err != nil {
				return err
			}
			// A main transcript's session goes with it (cascades to rollups).
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE source_path = ?`, p); err != nil {
				return err
			}
		}
		return nil
	})
	return int64(len(gone)), err
}

// RebuildRollups recomputes daily_usage, session_usage and hour_counts from
// the messages table. Small enough to do in full after every batch.
func (s *Store) RebuildRollups(ctx context.Context) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`DELETE FROM daily_usage`,
			`INSERT INTO daily_usage(date, model, msgs, tool_calls, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens)
			 SELECT d.date, d.model, d.msgs, COALESCE(t.cnt, 0), d.inp, d.outp, d.cr, d.cc
			 FROM (SELECT substr(ts, 1, 10) AS date, model, COUNT(*) AS msgs,
			              SUM(input_tokens) AS inp, SUM(output_tokens) AS outp,
			              SUM(cache_read_tokens) AS cr, SUM(cache_create_tokens) AS cc
			       FROM messages WHERE role = 'assistant' AND model IS NOT NULL AND ts <> ''
			       GROUP BY 1, 2) d
			 LEFT JOIN (SELECT substr(m.ts, 1, 10) AS date, m.model, COUNT(*) AS cnt
			            FROM tool_calls t JOIN messages m ON m.id = t.message_id
			            WHERE m.ts <> '' GROUP BY 1, 2) t
			   ON t.date = d.date AND t.model = d.model`,
			`DELETE FROM session_usage`,
			`INSERT INTO session_usage(session_id, models, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, duration_ms)
			 SELECT s.id,
			        COALESCE((SELECT GROUP_CONCAT(DISTINCT model) FROM messages WHERE session_id = s.id AND model IS NOT NULL), ''),
			        COALESCE(SUM(m.input_tokens), 0), COALESCE(SUM(m.output_tokens), 0),
			        COALESCE(SUM(m.cache_read_tokens), 0), COALESCE(SUM(m.cache_create_tokens), 0),
			        CASE WHEN s.started_at IS NULL OR s.ended_at IS NULL THEN 0
			             ELSE CAST(ROUND((julianday(s.ended_at) - julianday(s.started_at)) * 86400000) AS INTEGER) END
			 FROM sessions s LEFT JOIN messages m ON m.session_id = s.id AND m.role = 'assistant'
			 GROUP BY s.id`,
			`DELETE FROM hour_counts`,
			`INSERT INTO hour_counts(hour, count)
			 SELECT CAST(substr(ts, 12, 2) AS INTEGER), COUNT(*) FROM messages
			 WHERE role = 'user' AND kind = 'prompt' AND agent_id IS NULL AND ts <> '' GROUP BY 1`,
		}
		for _, q := range stmts {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetMeta stores a key/value pair.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetMeta returns the value for key or "".
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.rdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// LiveSessionRow mirrors sessions/<pid>.json.
type LiveSessionRow struct {
	PID                           int
	SessionID, CWD, Version, Name string
	StartedAt                     time.Time
}

// ReplaceLiveSessions replaces the live_sessions table wholesale.
func (s *Store) ReplaceLiveSessions(ctx context.Context, rows []LiveSessionRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM live_sessions`); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO live_sessions(pid, session_id, cwd, started_at, version, name, seen_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				r.PID, nullStr(r.SessionID), nullStr(r.CWD), FormatTime(r.StartedAt), nullStr(r.Version), nullStr(r.Name), now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// DirStatRow is the size of one top-level ~/.claude entry.
type DirStatRow struct {
	Name         string
	Files, Bytes int64
}

// ReplaceDirStats replaces dir_stats wholesale.
func (s *Store) ReplaceDirStats(ctx context.Context, rows []DirStatRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dir_stats`); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO dir_stats(name, file_count, bytes, computed_at) VALUES (?, ?, ?, ?)`,
				r.Name, r.Files, r.Bytes, now()); err != nil {
				return err
			}
		}
		return nil
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
