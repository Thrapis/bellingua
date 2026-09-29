-- Migration 4: translation memory. source_hash is an FNV-64a hash of the
-- plain source (store.SourceHash), backfilled in Go by migration 4's hook;
-- it turns "units with the same source" into an index lookup.

ALTER TABLE units ADD COLUMN source_hash INTEGER NOT NULL DEFAULT 0;
CREATE INDEX units_hash ON units (project_id, source_hash);
