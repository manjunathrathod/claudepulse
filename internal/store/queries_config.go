package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ---- writes (indexer) ----

// SettingsSnapshot is one settings file captured for display.
type SettingsSnapshot struct {
	Scope      string // user | project | local
	ProjectID  int64  // 0 for user scope
	Path       string
	SHA256     string
	JSON       string
	CapturedAt string
}

// PutSettingsSnapshot upserts a (redacted) settings document by path.
func PutSettingsSnapshot(ctx context.Context, tx *sql.Tx, scope string, projectID int64, path string, doc []byte) error {
	sum := sha256.Sum256(doc)
	var pid any
	if projectID > 0 {
		pid = projectID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO settings_snapshots(scope, project_id, path, sha256, json, captured_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET scope = excluded.scope, project_id = excluded.project_id,
			sha256 = excluded.sha256, json = excluded.json,
			captured_at = CASE WHEN settings_snapshots.sha256 = excluded.sha256 THEN settings_snapshots.captured_at ELSE excluded.captured_at END`,
		scope, pid, path, hex.EncodeToString(sum[:]), string(doc), now())
	return err
}

// DeleteSettingsSnapshot removes a snapshot whose file disappeared.
func DeleteSettingsSnapshot(ctx context.Context, tx *sql.Tx, path string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM settings_snapshots WHERE path = ?`, path)
	return err
}

// ProjectConfigRow mirrors project_config.
type ProjectConfigRow struct {
	ProjectID     int64
	ClaudeMDBytes int64
	Agents        []string
	Commands      []string
	Skills        []string
	MCPServers    []string
}

// PutProjectConfig upserts a project's config summary.
func PutProjectConfig(ctx context.Context, tx *sql.Tx, r ProjectConfigRow) error {
	j := func(v []string) string {
		if v == nil {
			v = []string{}
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO project_config(project_id, claude_md_bytes, agents, commands, skills, mcp_servers, captured_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET claude_md_bytes = excluded.claude_md_bytes, agents = excluded.agents,
			commands = excluded.commands, skills = excluded.skills, mcp_servers = excluded.mcp_servers, captured_at = excluded.captured_at`,
		r.ProjectID, r.ClaudeMDBytes, j(r.Agents), j(r.Commands), j(r.Skills), j(r.MCPServers), now())
	return err
}

// DeleteProjectConfig removes config rows for a project whose dir is gone.
func DeleteProjectConfig(ctx context.Context, tx *sql.Tx, projectID int64) error {
	for _, q := range []string{
		`DELETE FROM project_config WHERE project_id = ?`,
		`DELETE FROM settings_snapshots WHERE project_id = ?`,
		`DELETE FROM skills WHERE project_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, projectID); err != nil {
			return err
		}
	}
	return nil
}

// SkillRow mirrors skills.
type SkillRow struct {
	Path, Origin, Name, Description string
	ProjectID                       int64
}

// ReplaceSkills replaces every skill row of one origin (and project, for
// project-scoped skills) in a single transaction.
func ReplaceSkills(ctx context.Context, tx *sql.Tx, origin string, projectID int64, rows []SkillRow) error {
	var err error
	if projectID > 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM skills WHERE origin = ? AND project_id = ?`, origin, projectID)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM skills WHERE origin = ? AND project_id IS NULL`, origin)
	}
	if err != nil {
		return err
	}
	for _, r := range rows {
		var pid any
		if projectID > 0 {
			pid = projectID
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO skills(path, origin, project_id, name, description) VALUES (?, ?, ?, ?, ?)`,
			r.Path, origin, pid, r.Name, nullStr(r.Description)); err != nil {
			return err
		}
	}
	return nil
}

// PluginRow mirrors plugins (marketplaces, installed and synced plugins).
type PluginRow struct {
	Name, Kind, SourceKind, SourceRef, InstallLocation, LastUpdated, Version, Scope, Description string
	PluginCount                                                                                  int
}

// ReplacePlugins replaces the plugins table wholesale.
func (s *Store) ReplacePlugins(ctx context.Context, rows []PluginRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugins`); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugins(name, kind, source_kind, source_ref, install_location, last_updated, version, scope, plugin_count, description)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(kind, name, source_ref, scope) DO NOTHING`,
				r.Name, r.Kind, nullStr(r.SourceKind), r.SourceRef, nullStr(r.InstallLocation), nullStr(r.LastUpdated),
				nullStr(r.Version), r.Scope, r.PluginCount, nullStr(r.Description)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PlanRow mirrors plans.
type PlanRow struct {
	Slug, Title, MTime, SourcePath string
	Size                           int64
}

// ReplacePlans replaces the plans table wholesale.
func (s *Store) ReplacePlans(ctx context.Context, rows []PlanRow) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plans`); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO plans(slug, title, mtime, size, source_path) VALUES (?, ?, ?, ?, ?)`,
				r.Slug, r.Title, r.MTime, r.Size, r.SourcePath); err != nil {
				return err
			}
		}
		return nil
	})
}

// HistoryRow mirrors history_entries.
type HistoryRow struct {
	TS, ProjectPath, SessionID, Display string
}

// ReplaceHistory replaces history_entries wholesale and records the file
// cursor in the same transaction (the file is small and only re-read when it
// changes).
func (s *Store) ReplaceHistory(ctx context.Context, rows []HistoryRow, cursor ScanState) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_entries`); err != nil {
			return err
		}
		if err := PutScanState(ctx, tx, cursor); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO history_entries(ts, project_path, session_id, display) VALUES (?, ?, ?, ?)`,
				r.TS, nullStr(r.ProjectPath), nullStr(r.SessionID), r.Display); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- reads (handlers) ----

// ProjectConfigView joins a project with its config summary and snapshots.
type ProjectConfigView struct {
	ProjectID     int64
	Name          string // basename-able real path or encoded name
	RealPath      string
	ExistsOnDisk  bool
	ClaudeMDBytes int64
	Agents        []string
	Commands      []string
	Skills        []string
	MCPServers    []string
	HasSettings   bool
	HasLocal      bool
	LastActivity  string
}

// ListProjectConfigs returns every project with its config summary (projects
// without a readable directory have empty config and ExistsOnDisk=false).
func (s *Store) ListProjectConfigs(ctx context.Context) ([]ProjectConfigView, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT p.id, COALESCE(NULLIF(p.real_path, ''), p.encoded_name), COALESCE(p.real_path, ''),
		p.exists_on_disk, COALESCE(c.claude_md_bytes, 0), COALESCE(c.agents, '[]'), COALESCE(c.commands, '[]'),
		COALESCE(c.skills, '[]'), COALESCE(c.mcp_servers, '[]'),
		EXISTS(SELECT 1 FROM settings_snapshots ss WHERE ss.project_id = p.id AND ss.scope = 'project'),
		EXISTS(SELECT 1 FROM settings_snapshots ss WHERE ss.project_id = p.id AND ss.scope = 'local'),
		COALESCE(p.last_activity, '')
		FROM projects p LEFT JOIN project_config c ON c.project_id = p.id
		ORDER BY p.exists_on_disk DESC, p.last_activity DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectConfigView
	for rows.Next() {
		var v ProjectConfigView
		var exists, hasS, hasL int
		var agents, commands, skills, mcp string
		if err := rows.Scan(&v.ProjectID, &v.Name, &v.RealPath, &exists, &v.ClaudeMDBytes, &agents, &commands, &skills, &mcp,
			&hasS, &hasL, &v.LastActivity); err != nil {
			return nil, err
		}
		v.ExistsOnDisk, v.HasSettings, v.HasLocal = exists == 1, hasS == 1, hasL == 1
		json.Unmarshal([]byte(agents), &v.Agents)
		json.Unmarshal([]byte(commands), &v.Commands)
		json.Unmarshal([]byte(skills), &v.Skills)
		json.Unmarshal([]byte(mcp), &v.MCPServers)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ProjectSettings returns the project/local snapshots for one project.
func (s *Store) ProjectSettings(ctx context.Context, projectID int64) ([]SettingsSnapshot, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT scope, COALESCE(project_id, 0), path, sha256, json, captured_at
		FROM settings_snapshots WHERE project_id = ? ORDER BY scope`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettingsSnapshot
	for rows.Next() {
		var x SettingsSnapshot
		if err := rows.Scan(&x.Scope, &x.ProjectID, &x.Path, &x.SHA256, &x.JSON, &x.CapturedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SkillView is a skill for display.
type SkillView struct {
	Name, Description, Origin, Path string
	ProjectID                       int64
	ProjectName                     string
}

// ListSkills returns all skills, user/synced first then project-scoped.
func (s *Store) ListSkills(ctx context.Context) ([]SkillView, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT k.name, COALESCE(k.description, ''), k.origin, k.path, COALESCE(k.project_id, 0),
		COALESCE(NULLIF(p.real_path, ''), p.encoded_name, '')
		FROM skills k LEFT JOIN projects p ON p.id = k.project_id
		ORDER BY CASE k.origin WHEN 'user' THEN 0 WHEN 'synced' THEN 1 ELSE 2 END, k.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillView
	for rows.Next() {
		var v SkillView
		if err := rows.Scan(&v.Name, &v.Description, &v.Origin, &v.Path, &v.ProjectID, &v.ProjectName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListPlugins returns plugin rows of one kind (marketplace | installed | synced).
func (s *Store) ListPlugins(ctx context.Context, kind string) ([]PluginRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT name, kind, COALESCE(source_kind, ''), source_ref, COALESCE(install_location, ''),
		COALESCE(last_updated, ''), COALESCE(version, ''), scope, plugin_count, COALESCE(description, '')
		FROM plugins WHERE kind = ? ORDER BY name, source_ref, scope`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PluginRow
	for rows.Next() {
		var r PluginRow
		if err := rows.Scan(&r.Name, &r.Kind, &r.SourceKind, &r.SourceRef, &r.InstallLocation, &r.LastUpdated, &r.Version, &r.Scope, &r.PluginCount, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListPlans returns plans newest first.
func (s *Store) ListPlans(ctx context.Context) ([]PlanRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT slug, title, mtime, size, source_path FROM plans ORDER BY mtime DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanRow
	for rows.Next() {
		var r PlanRow
		if err := rows.Scan(&r.Slug, &r.Title, &r.MTime, &r.Size, &r.SourcePath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HistoryFilter narrows ListHistory.
type HistoryFilter struct {
	Query  string
	Limit  int
	Offset int
}

// HistoryView is one prompt-history row joined to its session/project.
type HistoryView struct {
	TS          string
	ProjectPath string
	SessionID   string
	Display     string
	HasSession  bool
	ProjectID   int64
}

// ListHistory returns prompt history newest first with the total match count.
func (s *Store) ListHistory(ctx context.Context, f HistoryFilter) ([]HistoryView, int64, error) {
	where, args := "", []any{}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = "WHERE h.display LIKE ?" + likeEscape
		args = append(args, "%"+escapeLike(q)+"%")
	}
	var total int64
	if err := s.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_entries h `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := ""
	if f.Limit > 0 {
		limit = fmt.Sprintf("LIMIT %d OFFSET %d", f.Limit, max(f.Offset, 0))
	}
	rows, err := s.rdb.QueryContext(ctx, `SELECT h.ts, COALESCE(h.project_path, ''), COALESCE(h.session_id, ''), h.display,
		s.id IS NOT NULL, COALESCE(s.project_id, 0)
		FROM history_entries h LEFT JOIN sessions s ON s.id = h.session_id `+where+` ORDER BY h.ts DESC `+limit, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []HistoryView
	for rows.Next() {
		var v HistoryView
		var has int
		if err := rows.Scan(&v.TS, &v.ProjectPath, &v.SessionID, &v.Display, &has, &v.ProjectID); err != nil {
			return nil, 0, err
		}
		v.HasSession = has == 1
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// TableCounts returns row counts for the system page.
func (s *Store) TableCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range []string{"projects", "sessions", "messages", "tool_calls", "subagents", "history_entries", "skills", "plugins", "plans", "settings_snapshots"} {
		var n int64
		if err := s.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}
