-- Migration 7: a project-wide history feed.
-- history gets the project (so the feed needs no join through units), the
-- value it replaced (old_target, old_state; NULL on the synthetic 'import'
-- baseline row, which is not a change) and an optional batch id shared by all
-- rows of one operation (a bulk change, a job, an import run).

ALTER TABLE history ADD COLUMN project_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE history ADD COLUMN old_target TEXT;
ALTER TABLE history ADD COLUMN old_state  INTEGER;
ALTER TABLE history ADD COLUMN batch      TEXT;

UPDATE history SET project_id = (SELECT u.project_id FROM units u WHERE u.id = history.unit_id);

-- The previous row of the same unit is what each row replaced.
UPDATE history AS h SET old_target = p.pt, old_state = p.ps
FROM (SELECT id, LAG(target) OVER w AS pt, LAG(state) OVER w AS ps
      FROM history WINDOW w AS (PARTITION BY unit_id ORDER BY id)) AS p
WHERE p.id = h.id;

-- Partial: the baseline rows are not changes. Queries must spell the WHERE term
-- exactly like this for the planner to use it.
CREATE INDEX history_proj ON history (project_id, at DESC, id DESC) WHERE old_state IS NOT NULL;
