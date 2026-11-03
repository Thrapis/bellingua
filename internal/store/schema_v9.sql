-- Migration 9: more list orders and search in reference languages.
--
-- units_status serves "needs work first" (state, then file order);
-- units_tlen serves the translation-length orders. Both are partial like the
-- other order indexes, so queries must spell `obsolete = 0` and, for
-- units_tlen, `target IS NOT NULL` exactly like this.
--
-- unit_langs_fts is a trigram index of the reference-language texts (a plain
-- FTS5 table: unit_langs has no rowid to point an external-content table at).
-- It has no triggers: LangWriter.Finish, DeleteProjectLang and DeleteProject
-- keep it current, and the backup strips and rebuilds it like units_fts.

CREATE INDEX units_status ON units (project_id, state, file_id, ord) WHERE obsolete = 0;
CREATE INDEX units_tlen ON units (project_id, length(target), id) WHERE obsolete = 0 AND target IS NOT NULL;

CREATE VIRTUAL TABLE unit_langs_fts USING fts5(
    target,
    unit_id UNINDEXED,
    lang UNINDEXED,
    tokenize = 'trigram'
);
INSERT INTO unit_langs_fts (target, unit_id, lang) SELECT target, unit_id, lang FROM unit_langs;
