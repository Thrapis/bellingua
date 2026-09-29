-- Migration 6: extra languages (English, Ukrainian, ... next to the main
-- target). project_langs lists them; locked = read-only reference.
-- unit_langs holds one text per unit and language, read one unit at a time,
-- so there is no FTS and there are no counters. source_hash is the hash of the
-- source the text was made from (store.SourceHash): when it differs from the
-- unit's current source_hash, the text is outdated.

CREATE TABLE project_langs (
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    lang        TEXT    NOT NULL,
    locked      INTEGER NOT NULL DEFAULT 1,
    ord         INTEGER NOT NULL DEFAULT 0,
    units       INTEGER NOT NULL DEFAULT 0, -- texts stored by the last import
    imported_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, lang)
) WITHOUT ROWID;

CREATE TABLE unit_langs (
    unit_id       INTEGER NOT NULL,
    lang          TEXT    NOT NULL,
    target        TEXT    NOT NULL,
    target_pieces BLOB,
    state         INTEGER NOT NULL,
    source_hash   INTEGER NOT NULL,
    PRIMARY KEY (unit_id, lang)
) WITHOUT ROWID;
