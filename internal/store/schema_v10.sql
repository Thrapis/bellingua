-- Migration 10: a project can be linked to a remote project (Crowdin) it was
-- pulled from. The access token is never stored here: it comes from the config
-- file or the environment, so a database backup never carries it.

CREATE TABLE project_links (
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL,           -- crowdin
    remote_id   INTEGER NOT NULL,           -- Crowdin project id
    identifier  TEXT    NOT NULL DEFAULT '', -- the slug in editor URLs, lower case
    base_url    TEXT    NOT NULL DEFAULT '', -- '' = crowdin.com; Enterprise API root otherwise
    source_lang TEXT    NOT NULL DEFAULT '', -- Crowdin language ids
    target_lang TEXT    NOT NULL DEFAULT '',
    last_pull   INTEGER NOT NULL DEFAULT 0,  -- unix seconds, 0 = never
    PRIMARY KEY (project_id, kind)
) WITHOUT ROWID;

CREATE INDEX project_links_identifier ON project_links (kind, identifier);
