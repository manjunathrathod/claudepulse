-- plugins.name alone is not unique: the same plugin can be installed from two
-- marketplaces or at two scopes, and a marketplace may share a plugin's name.
-- Rebuild with a composite key. Rows are fully replaced on every scan, so no
-- data needs to be carried over.

DROP TABLE plugins;

CREATE TABLE plugins (
    kind             TEXT NOT NULL,                   -- marketplace | installed | synced
    name             TEXT NOT NULL,
    source_ref       TEXT NOT NULL DEFAULT '',        -- marketplace name / repo
    scope            TEXT NOT NULL DEFAULT '',        -- user | project | ''
    source_kind      TEXT,
    install_location TEXT,
    last_updated     TEXT,
    version          TEXT,
    plugin_count     INTEGER NOT NULL DEFAULT 0,
    description      TEXT,
    PRIMARY KEY (kind, name, source_ref, scope)
);
