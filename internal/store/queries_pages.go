package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ProjectSummary is one row of the projects list.
type ProjectSummary struct {
	ID                 int64
	EncodedName        string
	RealPath           string
	ExistsOnDisk       bool
	HasClaudeMD        bool
	HasProjectSettings bool
	MemoryFiles        int
	Sessions           int64
	Prompts            int64
	ToolCalls          int64
	OutputTokens       int64
	FirstActivity      string
	LastActivity       string
}

const projectSummarySQL = `
SELECT p.id, p.encoded_name, COALESCE(p.real_path, ''), p.exists_on_disk, p.has_claude_md,
       p.has_project_settings, p.memory_file_count,
       COUNT(s.id), COALESCE(SUM(s.user_msg_count), 0), COALESCE(SUM(s.tool_call_count), 0),
       COALESCE(SUM(u.output_tokens), 0), COALESCE(MIN(s.started_at), ''), COALESCE(MAX(s.ended_at), '')
FROM projects p
LEFT JOIN sessions s ON s.project_id = p.id
LEFT JOIN session_usage u ON u.session_id = s.id
%s
GROUP BY p.id
%s`

func scanProjectSummary(sc interface{ Scan(...any) error }, p *ProjectSummary) error {
	var exists, claudeMD, settings int
	err := sc.Scan(&p.ID, &p.EncodedName, &p.RealPath, &exists, &claudeMD, &settings, &p.MemoryFiles,
		&p.Sessions, &p.Prompts, &p.ToolCalls, &p.OutputTokens, &p.FirstActivity, &p.LastActivity)
	p.ExistsOnDisk, p.HasClaudeMD, p.HasProjectSettings = exists == 1, claudeMD == 1, settings == 1
	return err
}

// ListProjects returns every project, most recently active first.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	q := fmt.Sprintf(projectSummarySQL, "", "ORDER BY MAX(s.ended_at) DESC NULLS LAST, p.encoded_name")
	rows, err := s.rdb.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectSummary
	for rows.Next() {
		var p ProjectSummary
		if err := scanProjectSummary(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns one project summary, or ok=false.
func (s *Store) GetProject(ctx context.Context, id int64) (ProjectSummary, bool, error) {
	var p ProjectSummary
	q := fmt.Sprintf(projectSummarySQL, "WHERE p.id = ?", "")
	err := scanProjectSummary(s.rdb.QueryRowContext(ctx, q, id), &p)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// SessionSummary is one row of a sessions list.
type SessionSummary struct {
	ID            string
	ProjectID     int64
	ProjectName   string // encoded name
	ProjectPath   string
	Title         string
	CLIVersion    string
	StartedAt     string
	EndedAt       string
	DurationMS    int64
	Prompts       int64
	AssistantMsgs int64
	ToolCalls     int64
	Subagents     int64
	Models        string
	OutputTokens  int64
	CacheRead     int64
}

const sessionSummarySQL = `
SELECT s.id, s.project_id, p.encoded_name, COALESCE(p.real_path, ''), COALESCE(s.title, ''),
       COALESCE(s.cli_version, ''), COALESCE(s.started_at, ''), COALESCE(s.ended_at, ''),
       COALESCE(u.duration_ms, 0), s.user_msg_count, s.assistant_msg_count, s.tool_call_count,
       s.subagent_count, COALESCE(u.models, ''), COALESCE(u.output_tokens, 0), COALESCE(u.cache_read_tokens, 0)
FROM sessions s
JOIN projects p ON p.id = s.project_id
LEFT JOIN session_usage u ON u.session_id = s.id
%s
ORDER BY s.started_at DESC
%s`

func (s *Store) querySessions(ctx context.Context, where, limit string, args ...any) ([]SessionSummary, error) {
	rows, err := s.rdb.QueryContext(ctx, fmt.Sprintf(sessionSummarySQL, where, limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var x SessionSummary
		if err := rows.Scan(&x.ID, &x.ProjectID, &x.ProjectName, &x.ProjectPath, &x.Title, &x.CLIVersion,
			&x.StartedAt, &x.EndedAt, &x.DurationMS, &x.Prompts, &x.AssistantMsgs, &x.ToolCalls,
			&x.Subagents, &x.Models, &x.OutputTokens, &x.CacheRead); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ListProjectSessions returns a project's sessions, newest first.
func (s *Store) ListProjectSessions(ctx context.Context, projectID int64) ([]SessionSummary, error) {
	return s.querySessions(ctx, "WHERE s.project_id = ?", "", projectID)
}

// RecentSessions returns the newest n sessions across all projects.
func (s *Store) RecentSessions(ctx context.Context, n int) ([]SessionSummary, error) {
	return s.querySessions(ctx, "", "LIMIT ?", n)
}

// ProjectToolUsage returns tool-call counts for one project.
func (s *Store) ProjectToolUsage(ctx context.Context, projectID int64, limit int) ([]ToolUsage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT t.name, COUNT(*) FROM tool_calls t
		JOIN sessions s ON s.id = t.session_id WHERE s.project_id = ?
		GROUP BY t.name ORDER BY 2 DESC LIMIT ?`, projectID, limit)
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

// ProjectModelUsage returns per-model usage for one project.
func (s *Store) ProjectModelUsage(ctx context.Context, projectID int64) ([]ModelUsage, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT m.model, COUNT(*), SUM(m.input_tokens), SUM(m.output_tokens),
		SUM(m.cache_read_tokens), SUM(m.cache_create_tokens)
		FROM messages m JOIN sessions s ON s.id = m.session_id
		WHERE s.project_id = ? AND m.role = 'assistant' AND m.model IS NOT NULL
		GROUP BY m.model ORDER BY 4 DESC`, projectID)
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
