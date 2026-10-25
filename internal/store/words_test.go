package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/model"
)

// A v7 database migrates to v8: units.words is backfilled from the pieces
// (codes are not words) and WordCounts sums it per state, skipping obsolete units.
func TestMigration8WordsBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for i := 0; i < 7; i++ {
		tx, _ := db.BeginTx(ctx, nil)
		if _, err := tx.ExecContext(ctx, migrations[i].sql); err != nil {
			t.Fatal(err)
		}
		if migrations[i].post != nil {
			if err := migrations[i].post(ctx, tx); err != nil {
				t.Fatal(err)
			}
		}
		tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
		tx.Commit()
	}
	withCode := []model.Piece{{Text: "Press "}, {Text: "{key}", Code: true}, {Text: " to jump"}}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, name, source_lang, target_lang, created_at) VALUES (3, 'p', 'ru', 'be', 0)`, nil},
		{`INSERT INTO files (id, project_id, path, dir, format) VALUES (1, 3, 'a.xlf', '', 'xliff')`, nil},
		{`INSERT INTO units (id, project_id, file_id, ord, key, source, state) VALUES (1, 3, 1, 0, 'a', 'one two three', 0)`, nil},
		{`INSERT INTO units (id, project_id, file_id, ord, key, source, source_pieces, target, state) VALUES (2, 3, 1, 1, 'b', ?, ?, 'x', 1)`,
			[]any{model.Plain(withCode), model.EncodePieces(withCode)}},
		{`INSERT INTO units (id, project_id, file_id, ord, key, source, target, state) VALUES (3, 3, 1, 2, 'c', 'four five', 'x', 3)`, nil},
		{`INSERT INTO units (id, project_id, file_id, ord, key, source, target, state, obsolete) VALUES (4, 3, 1, 3, 'd', 'gone away now', 'x', 2, 1)`, nil},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w, err := st.WordCounts(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := Words{Total: 3 + 3 + 2, MT: 3, Translated: 0, Approved: 2}
	if w != want {
		t.Errorf("WordCounts = %+v, want %+v", w, want)
	}
}

// The per-state word totals must come from the covering partial index.
func TestWordCountsUseCoveringIndex(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.R.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT state, sum(words) FROM units
		WHERE project_id = ? AND obsolete = 0 GROUP BY state`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "COVERING INDEX units_words") {
		t.Errorf("plan = %s", plan)
	}
}
