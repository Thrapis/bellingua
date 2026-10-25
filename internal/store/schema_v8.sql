-- Migration 8: word counts. units.words is the number of words of the source
-- (model.Words: codes are not words), kept by the importer and backfilled in
-- Go by migration 8's hook. The partial covering index answers the project's
-- per-state word totals without touching the table; queries must spell the
-- WHERE term exactly like this for the planner to use it.

ALTER TABLE units ADD COLUMN words INTEGER NOT NULL DEFAULT 0;
CREATE INDEX units_words ON units (project_id, state, words) WHERE obsolete = 0;
