-- Migration 1: initial schema. Triggers live in store.go (triggerDDL) because
-- bulk import drops and recreates them.

CREATE TABLE projects (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    source_lang TEXT    NOT NULL,
    target_lang TEXT    NOT NULL,
    settings    TEXT    NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL
);

-- Per-file counters are denormalised (maintained by triggers, or recomputed
-- once after a bulk import) so trees and progress bars never COUNT(*).
-- Untranslated = total - n_mt - n_translated - n_approved.
CREATE TABLE files (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL, -- slash-separated, relative to the import root
    dir          TEXT    NOT NULL, -- path's directory, '' at the root
    format       TEXT    NOT NULL, -- xliff | crowdin-csv
    original     TEXT    NOT NULL DEFAULT '', -- XLIFF <file original="...">
    total        INTEGER NOT NULL DEFAULT 0,
    n_mt         INTEGER NOT NULL DEFAULT 0,
    n_translated INTEGER NOT NULL DEFAULT 0,
    n_approved   INTEGER NOT NULL DEFAULT 0,
    n_err        INTEGER NOT NULL DEFAULT 0,
    n_warn       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (project_id, path)
);

-- source/target hold the plain rendered strings (search, export, FTS); the
-- *_pieces blobs carry the code/text split and are NULL when the string has
-- no codes. target IS NULL means "no translation".
CREATE TABLE units (
    id            INTEGER PRIMARY KEY,
    project_id    INTEGER NOT NULL,
    file_id       INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    ord           INTEGER NOT NULL,
    key           TEXT    NOT NULL,
    source        TEXT    NOT NULL,
    source_pieces BLOB,
    target        TEXT,
    target_pieces BLOB,
    context       TEXT    NOT NULL DEFAULT '',
    state         INTEGER NOT NULL DEFAULT 0,
    qa            INTEGER NOT NULL DEFAULT 0,
    changed       INTEGER NOT NULL DEFAULT 0, -- source changed by a re-import
    obsolete      INTEGER NOT NULL DEFAULT 0, -- gone from the latest import
    updated_at    INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX units_key ON units (file_id, key);
-- Keyset pagination: (project_id, file_id, ord) is the list order. The
-- trailing columns make it covering for counts and state/QA filters.
CREATE INDEX units_pos ON units (project_id, file_id, ord, obsolete, state, qa);
-- Partial indexes for the two hot filters ("to do", "has QA issue"); queries
-- must spell the WHERE terms exactly like this for the planner to use them.
CREATE INDEX units_todo ON units (project_id, file_id, ord, state, qa) WHERE state < 2 AND obsolete = 0;
CREATE INDEX units_qa ON units (project_id, file_id, ord, state, qa) WHERE qa <> 0 AND obsolete = 0;

CREATE TABLE history (
    id      INTEGER PRIMARY KEY,
    unit_id INTEGER NOT NULL REFERENCES units (id) ON DELETE CASCADE,
    target  TEXT,
    state   INTEGER NOT NULL,
    origin  TEXT    NOT NULL, -- import | edit | mt | bulk
    at      INTEGER NOT NULL
);
CREATE INDEX history_unit ON history (unit_id, id);

-- Substring search over Cyrillic: trigram tokenizer, external content.
CREATE VIRTUAL TABLE units_fts USING fts5(
    source, target,
    content = 'units', content_rowid = 'id',
    tokenize = 'trigram'
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
