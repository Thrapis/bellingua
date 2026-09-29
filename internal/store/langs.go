package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Thrapis/bellingua/internal/model"
)

// ProjectLang is an extra language of a project (see schema_v6.sql).
type ProjectLang struct {
	Lang       string `json:"lang"`
	Locked     bool   `json:"locked"`
	Units      int64  `json:"units"`
	ImportedAt int64  `json:"importedAt"`
}

// UnitLang is one extra-language text of a unit.
type UnitLang struct {
	Lang     string        `json:"lang"`
	Locked   bool          `json:"locked"`
	Target   []model.Piece `json:"tgt"`
	State    model.State   `json:"state"`
	Outdated bool          `json:"outdated"` // made from a different source
}

// ProjectLangs lists a project's extra languages in their display order.
func (s *Store) ProjectLangs(ctx context.Context, projectID int64) ([]ProjectLang, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT lang, locked, units, imported_at FROM project_langs
		WHERE project_id = ? ORDER BY ord, lang`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectLang{}
	for rows.Next() {
		var l ProjectLang
		if err := rows.Scan(&l.Lang, &l.Locked, &l.Units, &l.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UnitLangs returns a unit's extra-language texts, in the project's order.
func (s *Store) UnitLangs(ctx context.Context, unitID int64) ([]UnitLang, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT l.lang, pl.locked, l.target, l.target_pieces, l.state, l.source_hash <> u.source_hash
		FROM units u
		JOIN unit_langs l ON l.unit_id = u.id
		JOIN project_langs pl ON pl.project_id = u.project_id AND pl.lang = l.lang
		WHERE u.id = ? ORDER BY pl.ord, pl.lang`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnitLang{}
	for rows.Next() {
		var l UnitLang
		var text string
		var blob []byte
		if err := rows.Scan(&l.Lang, &l.Locked, &text, &blob, &l.State, &l.Outdated); err != nil {
			return nil, err
		}
		if l.Target, err = model.DecodePieces(blob, text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteProjectLang removes an extra language and its texts.
func (s *Store) DeleteProjectLang(ctx context.Context, projectID int64, lang string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM project_langs WHERE project_id = ? AND lang = ?", projectID, lang)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return deleteLangTexts(ctx, tx, projectID, lang)
	})
}

// deleteLangTexts removes a language's texts for all units of a project.
func deleteLangTexts(ctx context.Context, tx *sql.Tx, projectID int64, lang string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM unit_langs WHERE lang = ? AND unit_id IN
		(SELECT id FROM units WHERE project_id = ?)`, lang, projectID)
	return err
}

// ErrMainLang is returned when an extra language equals the project's own.
var ErrMainLang = errors.New("that is the project's source or target language")

// LangWriter stores the texts of one extra language inside a Write
// transaction: it replaces everything the language had.
type LangWriter struct {
	ctx       context.Context
	tx        *sql.Tx
	projectID int64
	lang      string
	byFile    *sql.Stmt
	ins       *sql.Stmt
	stored    int64
}

// UnitRef is an existing unit, found by key.
type UnitRef struct {
	ID         int64
	SourceHash int64
}

// NewLangWriter clears lang's texts in the project and prepares to store new
// ones. It registers the language (locked) if it is new.
func NewLangWriter(ctx context.Context, tx *sql.Tx, p *Project, lang string) (*LangWriter, error) {
	if lang == p.SourceLang || lang == p.TargetLang {
		return nil, ErrMainLang
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_langs (project_id, lang, ord)
		VALUES (?, ?, (SELECT count(*) FROM project_langs WHERE project_id = ?))
		ON CONFLICT (project_id, lang) DO NOTHING`, p.ID, lang, p.ID); err != nil {
		return nil, err
	}
	if err := deleteLangTexts(ctx, tx, p.ID, lang); err != nil {
		return nil, err
	}
	w := &LangWriter{ctx: ctx, tx: tx, projectID: p.ID, lang: lang}
	var err error
	if w.byFile, err = tx.PrepareContext(ctx, `SELECT u.key, u.id, u.source_hash FROM units u JOIN files f ON f.id = u.file_id
		WHERE f.project_id = ? AND f.path = ? AND u.obsolete = 0`); err != nil {
		return nil, err
	}
	if w.ins, err = tx.PrepareContext(ctx, `INSERT OR REPLACE INTO unit_langs (unit_id, lang, target, target_pieces, state, source_hash)
		VALUES (?, ?, ?, ?, ?, ?)`); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

// Units maps the keys of a project file's (non-obsolete) units; nil when the
// project has no such file.
func (w *LangWriter) Units(path string) (map[string]UnitRef, error) {
	rows, err := w.byFile.QueryContext(w.ctx, w.projectID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out map[string]UnitRef
	for rows.Next() {
		var key string
		var r UnitRef
		if err := rows.Scan(&key, &r.ID, &r.SourceHash); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]UnitRef{}
		}
		out[key] = r
	}
	return out, rows.Err()
}

// Put stores the language's text of a unit; sourceHash is SourceHash of the
// source it was made from.
func (w *LangWriter) Put(unitID int64, tgt []model.Piece, state model.State, sourceHash int64) error {
	_, err := w.ins.ExecContext(w.ctx, unitID, w.lang, model.Plain(tgt), model.EncodePieces(tgt), state, sourceHash)
	if err == nil {
		w.stored++
	}
	return err
}

// Finish records the import on the language.
func (w *LangWriter) Finish(now int64) error {
	_, err := w.tx.ExecContext(w.ctx, "UPDATE project_langs SET units = ?, imported_at = ? WHERE project_id = ? AND lang = ?",
		w.stored, now, w.projectID, w.lang)
	return err
}

// Close releases the prepared statements.
func (w *LangWriter) Close() {
	if w.byFile != nil {
		w.byFile.Close()
	}
	if w.ins != nil {
		w.ins.Close()
	}
}
