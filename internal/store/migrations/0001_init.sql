-- ClaudePulse initial schema. All timestamps are UTC RFC3339 text.

CREATE TABLE scan_state (
    path            TEXT PRIMARY KEY,
    size            INTEGER NOT NULL,
    mtime           TEXT NOT NULL,
    byte_offset     INTEGER NOT NULL,
    last_scanned_at TEXT NOT NULL
);

CREATE TABLE projects (
    id                   INTEGER PRIMARY KEY,
    encoded_name         TEXT NOT NULL UNIQUE,
    real_path            TEXT,
    exists_on_disk       INTEGER NOT NULL DEFAULT 0,
    has_claude_md        INTEGER NOT NULL DEFAULT 0,
    has_project_settings INTEGER NOT NULL DEFAULT 0,
    memory_file_count    INTEGER NOT NULL DEFAULT 0,
    first_seen           TEXT,
    last_activity        TEXT,
    source_path          TEXT NOT NULL
);

CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY,             -- session uuid
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title               TEXT,
    cwd                 TEXT,
    cli_version         TEXT,
    entrypoint          TEXT,
    started_at          TEXT,
    ended_at            TEXT,
    user_msg_count      INTEGER NOT NULL DEFAULT 0,   -- typed prompts only
    assistant_msg_count INTEGER NOT NULL DEFAULT 0,   -- deduped by message.id
    tool_call_count     INTEGER NOT NULL DEFAULT 0,
    subagent_count      INTEGER NOT NULL DEFAULT 0,
    source_path         TEXT NOT NULL
);
CREATE INDEX idx_sessions_project ON sessions(project_id, started_at);
CREATE INDEX idx_sessions_started ON sessions(started_at);

CREATE TABLE messages (
    id                  TEXT PRIMARY KEY,             -- msg_… (assistant) or line uuid (user)
    session_id          TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent_id            TEXT,                         -- non-null for subagent transcripts
    role                TEXT NOT NULL,                -- user | assistant
    kind                TEXT NOT NULL,                -- prompt | tool_result | mixed | response
    model               TEXT,
    ts                  TEXT,
    is_sidechain        INTEGER NOT NULL DEFAULT 0,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_create_tokens INTEGER NOT NULL DEFAULT 0,
    thinking_tokens     INTEGER NOT NULL DEFAULT 0,
    source_path         TEXT NOT NULL
);
CREATE INDEX idx_messages_session_ts ON messages(session_id, ts);
CREATE INDEX idx_messages_ts ON messages(ts);
CREATE INDEX idx_messages_source ON messages(source_path);

CREATE TABLE tool_calls (
    id          TEXT PRIMARY KEY,                     -- toolu_…
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    message_id  TEXT NOT NULL,
    agent_id    TEXT,
    name        TEXT NOT NULL,
    ts          TEXT,
    source_path TEXT NOT NULL
);
CREATE INDEX idx_tool_calls_session_name ON tool_calls(session_id, name);
CREATE INDEX idx_tool_calls_source ON tool_calls(source_path);

CREATE TABLE subagents (
    id          TEXT PRIMARY KEY,                     -- agent id
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent_type  TEXT,
    description TEXT,
    spawn_depth INTEGER,
    msg_count   INTEGER NOT NULL DEFAULT 0,
    source_path TEXT NOT NULL
);
CREATE INDEX idx_subagents_session ON subagents(session_id);

-- Materialised rollups, rebuilt by the indexer.
CREATE TABLE daily_usage (
    date                TEXT NOT NULL,
    model               TEXT NOT NULL,
    msgs                INTEGER NOT NULL,
    tool_calls          INTEGER NOT NULL,
    input_tokens        INTEGER NOT NULL,
    output_tokens       INTEGER NOT NULL,
    cache_read_tokens   INTEGER NOT NULL,
    cache_create_tokens INTEGER NOT NULL,
    PRIMARY KEY (date, model)
);

CREATE TABLE session_usage (
    session_id          TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    models              TEXT NOT NULL,                -- comma-separated
    input_tokens        INTEGER NOT NULL,
    output_tokens       INTEGER NOT NULL,
    cache_read_tokens   INTEGER NOT NULL,
    cache_create_tokens INTEGER NOT NULL,
    duration_ms         INTEGER NOT NULL
);

CREATE TABLE hour_counts (
    hour  INTEGER PRIMARY KEY,                        -- 0..23 UTC
    count INTEGER NOT NULL
);

-- Other file kinds.
CREATE TABLE history_entries (
    id           INTEGER PRIMARY KEY,
    ts           TEXT NOT NULL,
    project_path TEXT,
    session_id   TEXT,
    display      TEXT NOT NULL,
    UNIQUE (ts, session_id, display)
);
CREATE INDEX idx_history_ts ON history_entries(ts);

CREATE TABLE settings_snapshots (
    id          INTEGER PRIMARY KEY,
    scope       TEXT NOT NULL,                        -- user | project | local
    project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    path        TEXT NOT NULL UNIQUE,
    sha256      TEXT NOT NULL,
    json        TEXT NOT NULL,
    captured_at TEXT NOT NULL
);

CREATE TABLE plans (
    slug        TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    mtime       TEXT NOT NULL,
    size        INTEGER NOT NULL,
    source_path TEXT NOT NULL
);

CREATE TABLE skills (
    path        TEXT PRIMARY KEY,
    origin      TEXT NOT NULL,                        -- user | synced | project
    project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT
);

CREATE TABLE plugins (
    name             TEXT PRIMARY KEY,
    kind             TEXT NOT NULL,                   -- marketplace | synced
    source_kind      TEXT,
    source_ref       TEXT,
    install_location TEXT,
    last_updated     TEXT
);

CREATE TABLE live_sessions (
    pid        INTEGER PRIMARY KEY,
    session_id TEXT,
    cwd        TEXT,
    started_at TEXT,
    version    TEXT,
    name       TEXT,
    seen_at    TEXT NOT NULL
);

CREATE TABLE dir_stats (
    name        TEXT PRIMARY KEY,
    file_count  INTEGER NOT NULL,
    bytes       INTEGER NOT NULL,
    computed_at TEXT NOT NULL
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
