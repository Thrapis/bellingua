-- Migration 11: the order of projects on the home page is the user's to choose.
-- Existing projects keep the alphabetical order they had; new ones go last.

ALTER TABLE projects ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
UPDATE projects SET position = (SELECT count(*) FROM projects q WHERE q.name < projects.name);
