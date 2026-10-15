package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// A v6 database with history rows migrates to v7: every row gets its project,
// and each row's old_* is the previous row of the same unit (NULL on the
// baseline, which keeps it out of the feed).
func TestMigration7Backfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for i := 0; i < 6; i++ {
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
	for _, q := range []string{
		`INSERT INTO projects (id, name, source_lang, target_lang, created_at) VALUES (7, 'p', 'ru', 'be', 0)`,
		`INSERT INTO files (id, project_id, path, dir, format) VALUES (1, 7, 'a.xlf', '', 'xliff')`,
		`INSERT INTO units (id, project_id, file_id, ord, key, source, target, state) VALUES (1, 7, 1, 0, 'k', 's', 'c', 3)`,
		`INSERT INTO history (unit_id, target, state, origin, at) VALUES (1, 'a', 1, 'import', 10)`,
		`INSERT INTO history (unit_id, target, state, origin, at) VALUES (1, 'b', 2, 'edit', 20)`,
		`INSERT INTO history (unit_id, target, state, origin, at) VALUES (1, 'c', 3, 'bulk', 30)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.R.QueryContext(ctx, `SELECT project_id, old_target, old_state, state FROM history ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		oldT  sql.NullString
		oldS  sql.NullInt64
		state int
	}{
		{sql.NullString{}, sql.NullInt64{}, 1},
		{sql.NullString{String: "a", Valid: true}, sql.NullInt64{Int64: 1, Valid: true}, 2},
		{sql.NullString{String: "b", Valid: true}, sql.NullInt64{Int64: 2, Valid: true}, 3},
	}
	for i := 0; rows.Next(); i++ {
		var pid int64
		var oldT sql.NullString
		var oldS sql.NullInt64
		var state int
		if err := rows.Scan(&pid, &oldT, &oldS, &state); err != nil {
			t.Fatal(err)
		}
		if pid != 7 || oldT != want[i].oldT || oldS != want[i].oldS || state != want[i].state {
			t.Errorf("row %d = %d %v %v %d", i, pid, oldT, oldS, state)
		}
	}
	days, _, err := st.HistoryDays(ctx, 7, 0, 0, 5)
	if err != nil || len(days) != 1 || days[0].Count != 2 {
		t.Errorf("feed = %+v, %v (the baseline must not count)", days, err)
	}
}
