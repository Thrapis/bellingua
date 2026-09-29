package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Thrapis/bellingua/internal/model"
)

// TMRow is a human translation offered as translation memory.
type TMRow struct {
	UnitID    int64         `json:"unit"`
	Path      string        `json:"path"`
	Source    []model.Piece `json:"src"`
	Target    []model.Piece `json:"tgt"`
	State     model.State   `json:"state"`
	UpdatedAt int64         `json:"updatedAt"`
}

const tmCols = `u.id, f.path, u.source, u.source_pieces, u.target, u.target_pieces, u.state, u.updated_at`

func scanTM(rows *sql.Rows) ([]TMRow, error) {
	defer rows.Close()
	var out []TMRow
	for rows.Next() {
		var r TMRow
		var src, tgt string
		var sb, tb []byte
		if err := rows.Scan(&r.UnitID, &r.Path, &src, &sb, &tgt, &tb, &r.State, &r.UpdatedAt); err != nil {
			return nil, err
		}
		var err error
		if r.Source, err = model.DecodePieces(sb, src); err != nil {
			return nil, err
		}
		if r.Target, err = model.DecodePieces(tb, tgt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExactMatches returns human translations (translated/approved) of other
// units with exactly this source, best first (approved, then newest), one
// per distinct translation.
func (s *Store) ExactMatches(ctx context.Context, projectID, exceptUnit int64, source string, limit int) ([]TMRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+tmCols+` FROM units u JOIN files f ON f.id = u.file_id
		WHERE u.project_id = ? AND u.source_hash = ? AND u.source = ? AND u.id <> ?
			AND u.state >= 2 AND u.obsolete = 0 AND u.target IS NOT NULL
		ORDER BY u.state DESC, u.updated_at DESC LIMIT 50`, projectID, SourceHash(source), source, exceptUnit)
	if err != nil {
		return nil, err
	}
	all, err := scanTM(rows)
	if err != nil {
		return nil, err
	}
	var out []TMRow
	seen := map[string]bool{}
	for _, r := range all {
		k := model.Plain(r.Target)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// TMUnits loads the given units (human translations only) for display.
func (s *Store) TMUnits(ctx context.Context, ids []int64) ([]TMRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.R.QueryContext(ctx, `SELECT `+tmCols+` FROM units u JOIN files f ON f.id = u.file_id
		WHERE u.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`) AND u.state >= 2 AND u.target IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	return scanTM(rows)
}

// PendingDuplicates returns the other units with exactly this source that
// have no human translation yet (untranslated or machine).
func (s *Store) PendingDuplicates(ctx context.Context, projectID, exceptUnit int64, source string) ([]int64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id FROM units
		WHERE project_id = ? AND source_hash = ? AND source = ? AND id <> ? AND state < 2 AND obsolete = 0`,
		projectID, SourceHash(source), source, exceptUnit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FillCandidate is a unit without a human translation whose exact source
// has one elsewhere in the project.
type FillCandidate struct {
	UnitID int64
	Target string
}

// ExactFillCandidates finds, among units matching f that are untranslated
// or machine-translated, those with a 100% translation-memory match. The
// best match wins (approved over translated, then newest).
func (s *Store) ExactFillCandidates(ctx context.Context, f Filter) ([]FillCandidate, error) {
	where, args := f.where()
	rows, err := s.R.QueryContext(ctx, `SELECT u.id, (
			SELECT t.target FROM units t
			WHERE t.project_id = u.project_id AND t.source_hash = u.source_hash AND t.source = u.source
				AND t.state >= 2 AND t.obsolete = 0 AND t.target IS NOT NULL
			ORDER BY t.state DESC, t.updated_at DESC LIMIT 1)
		FROM units u WHERE `+where+` AND u.state < 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FillCandidate
	for rows.Next() {
		var c FillCandidate
		var tgt sql.NullString
		if err := rows.Scan(&c.UnitID, &tgt); err != nil {
			return nil, err
		}
		if tgt.Valid {
			c.Target = tgt.String
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// HumanUnits streams every non-obsolete human translation of a project
// (for building the fuzzy index).
func (s *Store) HumanUnits(ctx context.Context, projectID int64, fn func(id int64, source []model.Piece) error) error {
	rows, err := s.R.QueryContext(ctx, `SELECT id, source, source_pieces FROM units
		WHERE project_id = ? AND state >= 2 AND obsolete = 0 AND target IS NOT NULL`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var src string
		var sb []byte
		if err := rows.Scan(&id, &src, &sb); err != nil {
			return err
		}
		ps, err := model.DecodePieces(sb, src)
		if err != nil {
			return err
		}
		if err := fn(id, ps); err != nil {
			return err
		}
	}
	return rows.Err()
}
