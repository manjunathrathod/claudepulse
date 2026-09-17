-- Phase 4: per-project configuration and richer plugin rows.

CREATE TABLE project_config (
    project_id      INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    claude_md_bytes INTEGER NOT NULL DEFAULT 0,
    agents          TEXT NOT NULL DEFAULT '[]',   -- JSON array of agent names
    commands        TEXT NOT NULL DEFAULT '[]',   -- JSON array of command names
    skills          TEXT NOT NULL DEFAULT '[]',   -- JSON array of skill names
    mcp_servers     TEXT NOT NULL DEFAULT '[]',   -- JSON array of server names from .mcp.json
    captured_at     TEXT NOT NULL
);

ALTER TABLE plugins ADD COLUMN version TEXT;
ALTER TABLE plugins ADD COLUMN scope TEXT;
ALTER TABLE plugins ADD COLUMN plugin_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE plugins ADD COLUMN description TEXT;
