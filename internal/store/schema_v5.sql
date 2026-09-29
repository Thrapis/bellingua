-- Migration 5: glossary word forms. A JSON array of {"src","tgt"}: inflected
-- source forms of the term with their translations (glossary.Term.Forms).

ALTER TABLE terms ADD COLUMN forms TEXT NOT NULL DEFAULT '[]';
