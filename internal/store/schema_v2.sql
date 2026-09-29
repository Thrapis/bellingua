-- Migration 2: glossary.

CREATE TABLE terms (
    id             INTEGER PRIMARY KEY,
    project_id     INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    source         TEXT    NOT NULL,
    target         TEXT    NOT NULL DEFAULT '', -- alternatives separated by "|"
    note           TEXT    NOT NULL DEFAULT '',
    dnt            INTEGER NOT NULL DEFAULT 0,  -- do not translate
    case_sensitive INTEGER NOT NULL DEFAULT 0,
    updated_at     INTEGER NOT NULL,
    UNIQUE (project_id, source)
);

-- Bumped on every glossary change; caches of compiled glossaries key on it.
ALTER TABLE projects ADD COLUMN gloss_rev INTEGER NOT NULL DEFAULT 0;
