package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionFilter narrows ListSessions. Zero values mean "no constraint".
type SessionFilter struct {
	ProjectID int64
	Model     string // substring match against session_usage.models
	Since     time.Time
	Query     string // case-insensitive substring of the title
	Limit     int
	Offset    int
}

// ListSessions returns sessions matching f (newest first) and the total
// number of matches before Limit/Offset were applied.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) ([]SessionSummary, int64, error) {
	var where []string
	var args []any
	if f.ProjectID > 0 {
		where = append(where, "s.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Model != "" {
		where = append(where, "COALESCE(u.models, '') LIKE ?"+likeEscape)
		args = append(args, "%"+escapeLike(f.Model)+"%")
	}
	if !f.Since.IsZero() {
		where = append(where, "s.started_at >= ?")
		args = append(args, FormatTime(f.Since))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		// SQLite LIKE is already ASCII case-insensitive.
		where = append(where, "COALESCE(s.title, '') LIKE ?"+likeEscape)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQ := `SELECT COUNT(*) FROM sessions s LEFT JOIN session_usage u ON u.session_id = s.id ` + clause
	if err := s.rdb.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := ""
	if f.Limit > 0 {
		limit = fmt.Sprintf("LIMIT %d OFFSET %d", f.Limit, max(f.Offset, 0))
	}
	rows, err := s.querySessions(ctx, clause, limit, args...)
	return rows, total, err
}

// likeEscape is the ESCAPE clause paired with escapeLike.
const likeEscape = ` ESCAPE '\'`

// escapeLike makes user text match literally inside a LIKE pattern.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// SessionDetail is everything the session page needs beyond SessionSummary.
type SessionDetail struct {
	SessionSummary
	CWD         string
	Entrypoint  string
	SourcePath  string
	InputTokens int64
	CacheCreate int64
	Thinking    int64
}

// GetSession returns one session, or ok=false.
func (s *Store) GetSession(ctx context.Context, id string) (SessionDetail, bool, error) {
	var d SessionDetail
	rows, err := s.querySessions(ctx, "WHERE s.id = ?", "LIMIT 1", id)
	if err != nil {
		return d, false, err
	}
	if len(rows) == 0 {
		return d, false, nil
	}
	d.SessionSummary = rows[0]
	err = s.rdb.QueryRowContext(ctx, `SELECT COALESCE(cwd, ''), COALESCE(entrypoint, ''), source_path,
		COALESCE((SELECT SUM(input_tokens) FROM messages WHERE session_id = sessions.id), 0),
		COALESCE((SELECT SUM(cache_create_tokens) FROM messages WHERE session_id = sessions.id), 0),
		COALESCE((SELECT SUM(thinking_tokens) FROM messages WHERE session_id = sessions.id), 0)
		FROM sessions WHERE id = ?`, id).
		Scan(&d.CWD, &d.Entrypoint, &d.SourcePath, &d.InputTokens, &d.CacheCreate, &d.Thinking)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

// SessionToolUsage returns tool-call counts for one session.
func (s *Store) SessionToolUsage(ctx context.Context, id string, limit int) ([]ToolUsage, error) {
	q := `SELECT name, COUNT(*) FROM tool_calls WHERE session_id = ? GROUP BY name ORDER BY 2 DESC`
	args := []any{id}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolUsage
	for rows.Next() {
		var t ToolUsage
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SessionModelUsage returns per-model usage for one session.
func (s *Store) SessionModelUsage(ctx context.Context, id string) ([]ModelUsage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT model, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read_tokens), SUM(cache_create_tokens)
		FROM messages WHERE session_id = ? AND role = 'assistant' AND model IS NOT NULL
		GROUP BY model ORDER BY 4 DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Messages, &m.InputTokens, &m.OutputTokens, &m.CacheRead, &m.CacheCreate); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Subagent is one row of subagents for display.
type Subagent struct {
	ID           string
	AgentType    string
	Description  string
	SpawnDepth   int
	Messages     int64
	ToolCalls    int64
	OutputTokens int64
	StartedAt    string
	EndedAt      string
}

// SessionSubagents lists the subagents spawned by a session.
func (s *Store) SessionSubagents(ctx context.Context, id string) ([]Subagent, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT a.id, COALESCE(a.agent_type, ''), COALESCE(a.description, ''),
		COALESCE(a.spawn_depth, 0), a.msg_count,
		(SELECT COUNT(*) FROM tool_calls t WHERE t.session_id = a.session_id AND t.agent_id = a.id),
		COALESCE((SELECT SUM(output_tokens) FROM messages m WHERE m.session_id = a.session_id AND m.agent_id = a.id), 0),
		COALESCE((SELECT MIN(ts) FROM messages m WHERE m.session_id = a.session_id AND m.agent_id = a.id AND ts <> ''), ''),
		COALESCE((SELECT MAX(ts) FROM messages m WHERE m.session_id = a.session_id AND m.agent_id = a.id AND ts <> ''), '')
		FROM subagents a WHERE a.session_id = ? ORDER BY 8`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subagent
	for rows.Next() {
		var a Subagent
		if err := rows.Scan(&a.ID, &a.AgentType, &a.Description, &a.SpawnDepth, &a.Messages, &a.ToolCalls,
			&a.OutputTokens, &a.StartedAt, &a.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TimelineEvent is the minimal per-message record used to bucket a session.
type TimelineEvent struct {
	TS           time.Time
	Role         string // user | assistant
	Kind         string // prompt | tool_result | mixed | response
	Sidechain    bool
	OutputTokens int64
	ToolCalls    int64
}

// SessionEvents returns every message of a session in time order (main thread
// and subagents), each with its tool-call count.
func (s *Store) SessionEvents(ctx context.Context, id string) ([]TimelineEvent, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT m.ts, m.role, m.kind, m.is_sidechain, m.output_tokens,
		(SELECT COUNT(*) FROM tool_calls t WHERE t.session_id = m.session_id AND t.message_id = m.id)
		FROM messages m WHERE m.session_id = ? AND m.ts <> '' ORDER BY m.ts`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineEvent
	for rows.Next() {
		var e TimelineEvent
		var ts string
		var side int
		if err := rows.Scan(&ts, &e.Role, &e.Kind, &side, &e.OutputTokens, &e.ToolCalls); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		e.TS, e.Sidechain = t, side == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ProjectOption is a (id, name) pair for filter dropdowns.
type ProjectOption struct {
	ID       int64
	Name     string // real path if known, else encoded name
	Sessions int64
}

// ProjectOptions lists projects that have at least one session, by name.
func (s *Store) ProjectOptions(ctx context.Context) ([]ProjectOption, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT p.id, COALESCE(NULLIF(p.real_path, ''), p.encoded_name), COUNT(s.id)
		FROM projects p JOIN sessions s ON s.project_id = p.id GROUP BY p.id ORDER BY 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectOption
	for rows.Next() {
		var o ProjectOption
		if err := rows.Scan(&o.ID, &o.Name, &o.Sessions); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ModelOptions lists distinct models with usage, most used first.
func (s *Store) ModelOptions(ctx context.Context) ([]string, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT model FROM messages WHERE role = 'assistant' AND model IS NOT NULL
		AND output_tokens > 0 GROUP BY model ORDER BY SUM(output_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
