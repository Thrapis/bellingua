package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/Thrapis/bellingua/internal/glossary"
)

// ErrDuplicate is returned when a term with the same source already exists.
var ErrDuplicate = errors.New("a term with this source already exists")

// ErrInvalid is returned for a term without a source.
var ErrInvalid = errors.New("term source is empty")

const termCols = "id, source, target, note, dnt, case_sensitive, forms, updated_at"

func scanTerm(sc interface{ Scan(...any) error }) (glossary.Term, error) {
	var t glossary.Term
	var forms string
	if err := sc.Scan(&t.ID, &t.Source, &t.Target, &t.Note, &t.DNT, &t.CaseSensitive, &forms, &t.UpdatedAt); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(forms), &t.Forms); err != nil || t.Forms == nil {
		t.Forms = []glossary.Form{} // a damaged value must not hide the term
	}
	return t, nil
}

func formsJSON(fs []glossary.Form) string {
	if len(fs) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(fs)
	return string(b)
}

// Terms lists a project's glossary, ordered by source.
func (s *Store) Terms(ctx context.Context, projectID int64) ([]glossary.Term, error) {
	rows, err := s.R.QueryContext(ctx, "SELECT "+termCols+" FROM terms WHERE project_id = ? ORDER BY lower(source), source", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []glossary.Term{}
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GlossaryRev returns the project's glossary revision.
func (s *Store) GlossaryRev(ctx context.Context, projectID int64) (int64, error) {
	var rev int64
	err := s.R.QueryRowContext(ctx, "SELECT gloss_rev FROM projects WHERE id = ?", projectID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

// Glossary loads a project's terms into a matcher.
func (s *Store) Glossary(ctx context.Context, p *Project) (*glossary.Glossary, error) {
	terms, err := s.Terms(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return glossary.New(p.SourceLang, terms), nil
}

func cleanTerm(t *glossary.Term) error {
	t.Source = strings.TrimSpace(t.Source)
	t.Note = strings.TrimSpace(t.Note)
	t.Target = strings.Join(t.Targets(), " | ")
	if t.Source == "" {
		return ErrInvalid
	}
	// Forms: trimmed, complete, one per source form (the last one wins).
	forms := []glossary.Form{}
	idx := map[string]int{}
	for _, f := range t.Forms {
		f.Src, f.Tgt = strings.TrimSpace(f.Src), strings.TrimSpace(f.Tgt)
		if f.Src == "" || f.Tgt == "" {
			continue
		}
		k := strings.ToLower(f.Src)
		if i, ok := idx[k]; ok {
			forms[i] = f
			continue
		}
		idx[k] = len(forms)
		forms = append(forms, f)
	}
	t.Forms = forms
	return nil
}

func isUniqueErr(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func bumpGlossary(ctx context.Context, tx *sql.Tx, projectID int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE projects SET gloss_rev = gloss_rev + 1 WHERE id = ?", projectID)
	return err
}

// CreateTerm adds a term.
func (s *Store) CreateTerm(ctx context.Context, projectID int64, t glossary.Term) (glossary.Term, error) {
	if err := cleanTerm(&t); err != nil {
		return t, err
	}
	t.UpdatedAt = time.Now().Unix()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO terms (project_id, source, target, note, dnt, case_sensitive, forms, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, t.Source, t.Target, t.Note, t.DNT, t.CaseSensitive, formsJSON(t.Forms), t.UpdatedAt)
		if isUniqueErr(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		if t.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return bumpGlossary(ctx, tx, projectID)
	})
	return t, err
}

// TermProject returns the project a term belongs to.
func (s *Store) TermProject(ctx context.Context, id int64) (int64, error) {
	var pid int64
	err := s.R.QueryRowContext(ctx, "SELECT project_id FROM terms WHERE id = ?", id).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return pid, err
}

// UpdateTerm replaces a term's fields.
func (s *Store) UpdateTerm(ctx context.Context, id int64, t glossary.Term) (glossary.Term, error) {
	if err := cleanTerm(&t); err != nil {
		return t, err
	}
	t.ID, t.UpdatedAt = id, time.Now().Unix()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM terms WHERE id = ?", id).Scan(&pid); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE terms SET source = ?, target = ?, note = ?, dnt = ?, case_sensitive = ?, forms = ?, updated_at = ?
			WHERE id = ?`, t.Source, t.Target, t.Note, t.DNT, t.CaseSensitive, formsJSON(t.Forms), t.UpdatedAt, id)
		if isUniqueErr(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		return bumpGlossary(ctx, tx, pid)
	})
	return t, err
}

// DeleteTerm removes a term.
func (s *Store) DeleteTerm(ctx context.Context, id int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var pid int64
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM terms WHERE id = ?", id).Scan(&pid); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM terms WHERE id = ?", id); err != nil {
			return err
		}
		return bumpGlossary(ctx, tx, pid)
	})
}

// UpsertTerms inserts terms, or updates the existing term with the same
// source (a CSV import). Terms without a source are skipped.
func (s *Store) UpsertTerms(ctx context.Context, projectID int64, terms []glossary.Term) (inserted, updated int, err error) {
	now := time.Now().Unix()
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, t := range terms {
			if cleanTerm(&t) != nil {
				continue
			}
			res, err := tx.ExecContext(ctx, `UPDATE terms SET target = ?, note = ?, dnt = ?, case_sensitive = ?, forms = ?, updated_at = ?
				WHERE project_id = ? AND source = ?`, t.Target, t.Note, t.DNT, t.CaseSensitive, formsJSON(t.Forms), now, projectID, t.Source)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				updated++
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO terms (project_id, source, target, note, dnt, case_sensitive, forms, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, t.Source, t.Target, t.Note, t.DNT, t.CaseSensitive, formsJSON(t.Forms), now); err != nil {
				return err
			}
			inserted++
		}
		return bumpGlossary(ctx, tx, projectID)
	})
	return inserted, updated, err
}

// termFormsLimit caps the strings scanned for a term's forms: enough to see
// every form a large project uses, and still instant.
const termFormsLimit = 5000

// TermForms finds the inflected forms of source that occur in the project's
// source strings, most frequent first (the base form itself is left out).
func (s *Store) TermForms(ctx context.Context, p *Project, source string) ([]glossary.FormCount, error) {
	key := glossary.SearchKey(p.SourceLang, source)
	if key == "" {
		return []glossary.FormCount{}, nil
	}
	q := "SELECT u.source FROM units u WHERE u.project_id = ? AND u.obsolete = 0 AND "
	args := []any{p.ID}
	if utf8.RuneCountInString(key) >= 3 {
		// Trigram FTS is case-insensitive, like the stem.
		q += "u.id IN (SELECT rowid FROM units_fts WHERE units_fts MATCH ?)"
		args = append(args, "{source} : "+ftsPhrase(key))
	} else {
		q += "instr(lower(u.source), ?) > 0"
		args = append(args, key)
	}
	q += " LIMIT ?"
	args = append(args, termFormsLimit)
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c := glossary.NewFormCounter(p.SourceLang, source)
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		c.Add(src)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return c.Result(), nil
}
