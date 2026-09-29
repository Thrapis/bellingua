package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Thrapis/bellingua/internal/qa"
)

// Settings are per-project options stored as JSON.
type Settings struct {
	QA qa.Config `json:"qa"`
}

// Counts are progress counters (of non-obsolete units).
type Counts struct {
	Total      int64 `json:"total"`
	MT         int64 `json:"mt"`
	Translated int64 `json:"translated"`
	Approved   int64 `json:"approved"`
	Errors     int64 `json:"errors"`
	Warnings   int64 `json:"warnings"`
}

// Add accumulates o into c.
func (c *Counts) Add(o Counts) {
	c.Total += o.Total
	c.MT += o.MT
	c.Translated += o.Translated
	c.Approved += o.Approved
	c.Errors += o.Errors
	c.Warnings += o.Warnings
}

// Project is one translation project (one source→target language pair).
type Project struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	SourceLang string   `json:"sourceLang"`
	TargetLang string   `json:"targetLang"`
	Settings   Settings `json:"settings"`
	Counts     Counts   `json:"counts"`
	Files      int64    `json:"files"`
	GlossRev   int64    `json:"glossRev"` // bumped on every glossary change
}

const projectCols = `p.id, p.name, p.source_lang, p.target_lang, p.settings, p.gloss_rev,
	count(f.id), coalesce(sum(f.total),0), coalesce(sum(f.n_mt),0), coalesce(sum(f.n_translated),0),
	coalesce(sum(f.n_approved),0), coalesce(sum(f.n_err),0), coalesce(sum(f.n_warn),0)`

func scanProject(sc interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var settings string
	c := &p.Counts
	if err := sc.Scan(&p.ID, &p.Name, &p.SourceLang, &p.TargetLang, &settings, &p.GlossRev,
		&p.Files, &c.Total, &c.MT, &c.Translated, &c.Approved, &c.Errors, &c.Warnings); err != nil {
		return nil, err
	}
	p.Settings = Settings{QA: qa.DefaultConfig(p.TargetLang)}
	if err := json.Unmarshal([]byte(settings), &p.Settings); err != nil {
		return nil, err
	}
	return &p, nil
}

// Projects lists all projects with their counters.
func (s *Store) Projects(ctx context.Context) ([]*Project, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+projectCols+`
		FROM projects p LEFT JOIN files f ON f.project_id = p.id GROUP BY p.id ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Project returns one project.
func (s *Store) Project(ctx context.Context, id int64) (*Project, error) {
	p, err := scanProject(s.R.QueryRowContext(ctx, `SELECT `+projectCols+`
		FROM projects p LEFT JOIN files f ON f.project_id = p.id WHERE p.id = ? GROUP BY p.id`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ProjectByName looks a project up by name.
func (s *Store) ProjectByName(ctx context.Context, name string) (*Project, error) {
	var id int64
	err := s.R.QueryRowContext(ctx, "SELECT id FROM projects WHERE name = ?", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, id)
}

// CreateProject inserts a project with default settings.
func (s *Store) CreateProject(ctx context.Context, name, sourceLang, targetLang string) (*Project, error) {
	settings, _ := json.Marshal(Settings{QA: qa.DefaultConfig(targetLang)})
	var id int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO projects (name, source_lang, target_lang, settings, created_at)
			VALUES (?, ?, ?, ?, ?)`, name, sourceLang, targetLang, string(settings), time.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, id)
}

// SaveSettings replaces a project's settings.
func (s *Store) SaveSettings(ctx context.Context, id int64, st Settings) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE projects SET settings = ? WHERE id = ?", string(b), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteProject removes a project and everything in it.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := BeginBulk(ctx, tx); err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM history WHERE unit_id IN (SELECT id FROM units WHERE project_id = ?)",
			"DELETE FROM unit_langs WHERE unit_id IN (SELECT id FROM units WHERE project_id = ?)",
			"DELETE FROM project_langs WHERE project_id = ?",
			"DELETE FROM units WHERE project_id = ?",
			"DELETE FROM files WHERE project_id = ?",
			"DELETE FROM projects WHERE id = ?",
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return EndBulk(ctx, tx, id)
	})
}

// File is one imported file with its counters.
type File struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	Dir      string `json:"dir"`
	Format   string `json:"format"`
	Original string `json:"-"`
	Counts
}

// Files lists a project's files ordered by path (= id order after import).
func (s *Store) Files(ctx context.Context, projectID int64) ([]*File, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, path, dir, format, original,
		total, n_mt, n_translated, n_approved, n_err, n_warn
		FROM files WHERE project_id = ? ORDER BY path`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.Path, &f.Dir, &f.Format, &f.Original,
			&f.Total, &f.MT, &f.Translated, &f.Approved, &f.Errors, &f.Warnings); err != nil {
			return nil, err
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// SetMeta stores a key/value pair.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta reads a key, "" if absent.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.R.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
