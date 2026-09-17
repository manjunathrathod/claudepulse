package store

import (
	"context"
	"database/sql"
	"time"
)

// Totals are the headline numbers for the dashboard.
type Totals struct {
	Projects      int64  `json:"projects"`
	Sessions      int64  `json:"sessions"`
	UserPrompts   int64  `json:"user_prompts"`
	AssistantMsgs int64  `json:"assistant_messages"`
	ToolCalls     int64  `json:"tool_calls"`
	Subagents     int64  `json:"subagents"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	CacheRead     int64  `json:"cache_read_tokens"`
	CacheCreate   int64  `json:"cache_create_tokens"`
	FirstActivity string `json:"first_activity,omitempty"`
	LastActivity  string `json:"last_activity,omitempty"`
}

// GetTotals computes the headline numbers.
func (s *Store) GetTotals(ctx context.Context) (Totals, error) {
	var t Totals
	var first, last sql.NullString
	err := s.rdb.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM projects),
		(SELECT COUNT(*) FROM sessions),
		(SELECT COUNT(*) FROM messages WHERE role = 'user' AND kind = 'prompt' AND agent_id IS NULL),
		(SELECT COUNT(*) FROM messages WHERE role = 'assistant'),
		(SELECT COUNT(*) FROM tool_calls),
		(SELECT COUNT(*) FROM subagents),
		COALESCE((SELECT SUM(input_tokens) FROM messages), 0),
		COALESCE((SELECT SUM(output_tokens) FROM messages), 0),
		COALESCE((SELECT SUM(cache_read_tokens) FROM messages), 0),
		COALESCE((SELECT SUM(cache_create_tokens) FROM messages), 0),
		(SELECT MIN(started_at) FROM sessions), (SELECT MAX(ended_at) FROM sessions)`).
		Scan(&t.Projects, &t.Sessions, &t.UserPrompts, &t.AssistantMsgs, &t.ToolCalls, &t.Subagents,
			&t.InputTokens, &t.OutputTokens, &t.CacheRead, &t.CacheCreate, &first, &last)
	t.FirstActivity, t.LastActivity = first.String, last.String
	return t, err
}

// ModelUsage is token usage for one model.
type ModelUsage struct {
	Model        string `json:"model"`
	Messages     int64  `json:"messages"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheCreate  int64  `json:"cache_create_tokens"`
}

// GetModelUsage returns usage grouped by model, largest output first.
func (s *Store) GetModelUsage(ctx context.Context) ([]ModelUsage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT model, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read_tokens), SUM(cache_create_tokens)
		FROM messages WHERE role = 'assistant' AND model IS NOT NULL GROUP BY model ORDER BY 4 DESC`)
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

// DailyUsage is one row of the daily rollup.
type DailyUsage struct {
	Date         string `json:"date"`
	Model        string `json:"model"`
	Messages     int64  `json:"messages"`
	ToolCalls    int64  `json:"tool_calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheCreate  int64  `json:"cache_create_tokens"`
}

// GetDailyUsage returns rollup rows for the last `days` days (0 = all).
func (s *Store) GetDailyUsage(ctx context.Context, days int) ([]DailyUsage, error) {
	q := `SELECT date, model, msgs, tool_calls, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens
		FROM daily_usage`
	var args []any
	if days > 0 {
		q += ` WHERE date >= ?`
		args = append(args, time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02"))
	}
	q += ` ORDER BY date, model`
	rows, err := s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.Model, &d.Messages, &d.ToolCalls, &d.InputTokens, &d.OutputTokens, &d.CacheRead, &d.CacheCreate); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetHourCounts returns prompt counts per UTC hour, always 24 entries.
func (s *Store) GetHourCounts(ctx context.Context) ([24]int64, error) {
	var out [24]int64
	rows, err := s.rdb.QueryContext(ctx, `SELECT hour, count FROM hour_counts`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h int
		var c int64
		if err := rows.Scan(&h, &c); err != nil {
			return out, err
		}
		if h >= 0 && h < 24 {
			out[h] = c
		}
	}
	return out, rows.Err()
}

// ToolUsage is the call count for one tool name.
type ToolUsage struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// GetToolUsage returns the top tools by call count (limit 0 = all).
func (s *Store) GetToolUsage(ctx context.Context, limit int) ([]ToolUsage, error) {
	q := `SELECT name, COUNT(*) FROM tool_calls GROUP BY name ORDER BY 2 DESC`
	var args []any
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

// LiveSession is a row of live_sessions for display.
type LiveSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	StartedAt string `json:"started_at"`
	Version   string `json:"version"`
	Name      string `json:"name"`
}

// GetLiveSessions lists currently running Claude Code processes.
func (s *Store) GetLiveSessions(ctx context.Context) ([]LiveSession, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT pid, COALESCE(session_id,''), COALESCE(cwd,''), COALESCE(started_at,''),
		COALESCE(version,''), COALESCE(name,'') FROM live_sessions ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveSession
	for rows.Next() {
		var l LiveSession
		if err := rows.Scan(&l.PID, &l.SessionID, &l.CWD, &l.StartedAt, &l.Version, &l.Name); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DirStat is a row of dir_stats for display.
type DirStat struct {
	Name  string `json:"name"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// GetDirStats lists top-level directory sizes, largest first.
func (s *Store) GetDirStats(ctx context.Context) ([]DirStat, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT name, file_count, bytes FROM dir_stats ORDER BY bytes DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirStat
	for rows.Next() {
		var d DirStat
		if err := rows.Scan(&d.Name, &d.Files, &d.Bytes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountSessions is used by /healthz.
func (s *Store) CountSessions(ctx context.Context) (int64, error) {
	var n int64
	err := s.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}
