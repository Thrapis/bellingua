-- Migration 3: indexes for the non-default list orders (keyset paging).
-- The list query spells `obsolete = 0` and `length(u.source)` exactly like
-- this so the planner can use them.

CREATE INDEX units_updated ON units (project_id, updated_at, id) WHERE obsolete = 0;
CREATE INDEX units_len ON units (project_id, length(source), id) WHERE obsolete = 0;
