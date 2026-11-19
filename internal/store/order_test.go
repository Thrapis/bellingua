package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func names(t *testing.T, st *Store) []string {
	t.Helper()
	ps, err := st.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// New projects go last; SetProjectOrder moves the listed ones to the front in
// that order and keeps the rest after them as they were.
func TestProjectOrder(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids := map[string]int64{}
	for _, n := range []string{"zeta", "alpha", "mid"} {
		p, err := st.CreateProject(ctx, n, "en", "be")
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = p.ID
	}
	if got := names(t, st); !slices.Equal(got, []string{"zeta", "alpha", "mid"}) {
		t.Fatalf("new projects must follow creation order, got %v", got)
	}

	if err := st.SetProjectOrder(ctx, []int64{ids["mid"], ids["zeta"], ids["alpha"]}); err != nil {
		t.Fatal(err)
	}
	if got := names(t, st); !slices.Equal(got, []string{"mid", "zeta", "alpha"}) {
		t.Fatalf("after reorder: %v", got)
	}
	// A project created later goes to the end of the chosen order.
	if _, err := st.CreateProject(ctx, "new", "en", "be"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, st); !slices.Equal(got, []string{"mid", "zeta", "alpha", "new"}) {
		t.Fatalf("new project not last: %v", got)
	}
	// A shorter list moves only those to the front.
	if err := st.SetProjectOrder(ctx, []int64{ids["alpha"]}); err != nil {
		t.Fatal(err)
	}
	if got := names(t, st); !slices.Equal(got, []string{"alpha", "mid", "zeta", "new"}) {
		t.Fatalf("partial reorder: %v", got)
	}
	// Unknown or repeated ids are refused and change nothing.
	for _, bad := range [][]int64{{9999}, {ids["mid"], ids["mid"]}} {
		if err := st.SetProjectOrder(ctx, bad); !errors.Is(err, ErrNotFound) && err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if got := names(t, st); !slices.Equal(got, []string{"alpha", "mid", "zeta", "new"}) {
		t.Fatalf("a refused order changed the list: %v", got)
	}
}

// A database from before migration 11 keeps its alphabetical order.
func TestMigration11KeepsNameOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v10.db")
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for i := 0; i < 10; i++ {
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
	for _, n := range []string{"c", "a", "b"} { // created out of name order
		if _, err := db.ExecContext(ctx, `INSERT INTO projects (name, source_lang, target_lang, created_at) VALUES (?, 'en', 'be', 0)`, n); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := names(t, st); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("migrated order = %v", got)
	}
}
