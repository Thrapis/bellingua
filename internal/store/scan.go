package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Thrapis/bellingua/internal/model"
)

// ScanTexts calls fn with the text of every unit matching f that has one:
// the translation (target = true) or the source. Codes are left out (text
// pieces are joined by a space), so markup is never taken for words.
// It returns how many units were scanned.
func (s *Store) ScanTexts(ctx context.Context, f Filter, target bool, fn func(text string)) (int, error) {
	where, args := f.where()
	col := "source"
	if target {
		col = "target"
		where += " AND u.target IS NOT NULL"
	}
	rows, err := s.R.QueryContext(ctx, `SELECT u.`+col+`, u.`+col+`_pieces FROM units u WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var text string
		var blob []byte
		if err := rows.Scan(&text, &blob); err != nil {
			return n, err
		}
		if blob != nil { // the string has codes
			ps, err := model.DecodePieces(blob, text)
			if err != nil {
				return n, err
			}
			var b strings.Builder
			for _, p := range ps {
				if !p.Code {
					b.WriteString(p.Text)
					b.WriteByte(' ')
				}
			}
			text = b.String()
		}
		fn(text)
		n++
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return n, err
			}
		}
	}
	return n, rows.Err()
}

// ExportRow is one unit with its whole texts (codes inline).
type ExportRow struct {
	ID        int64
	State     model.State
	UpdatedAt int64
	Path      string
	Key       string
	Source    string
	Target    *string
}

// ExportUnits calls fn for every unit matching f in file order, with the
// complete texts. Stops at the first error from fn.
func (s *Store) ExportUnits(ctx context.Context, f Filter, fn func(ExportRow) error) error {
	where, args := f.where()
	rows, err := s.R.QueryContext(ctx, `SELECT u.id, u.state, u.updated_at, fl.path, u.key, u.source, u.target
		FROM units u JOIN files fl ON fl.id = u.file_id
		WHERE `+where+` ORDER BY u.project_id, u.file_id, u.ord`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var r ExportRow
		var t sql.NullString
		if err := rows.Scan(&r.ID, &r.State, &r.UpdatedAt, &r.Path, &r.Key, &r.Source, &t); err != nil {
			return err
		}
		if t.Valid {
			r.Target = &t.String
		}
		if err := fn(r); err != nil {
			return err
		}
		if n++; n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
