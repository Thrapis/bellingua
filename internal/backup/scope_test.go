package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/store"
)

// A snapshot of chosen projects holds only those (rows, history, search
// index), is marked partial, and refuses to replace a database by accident.
func TestScopedSnapshot(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "alpha")
	defer st.Close()
	for _, n := range []string{"beta", "gamma"} {
		if _, err := st.CreateProject(ctx, n, "ru", "be"); err != nil {
			t.Fatal(err)
		}
	}
	addUnit(t, st, "alpha", "Привет, мир")
	addUnit(t, st, "beta", "Секретный проект")
	addUnit(t, st, "gamma", "Третий проект")

	target := LocalDir(t.TempDir())
	if _, err := RunScoped(ctx, st.Path, []Target{target}, 5, []string{"gamma", "alpha", "no-such"}, nil); err != nil {
		t.Fatal(err)
	}

	// Restoring a partial snapshot needs an explicit yes.
	dst := filepath.Join(t.TempDir(), "restored.db")
	os.WriteFile(dst, []byte("old"), 0o644)
	err := Restore(ctx, target, "latest", dst)
	var pe *PartialError
	if !errors.As(err, &pe) || !slices.Equal(pe.Projects, []string{"alpha", "gamma"}) {
		t.Fatalf("restore without consent: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Fatal("the database was replaced despite the refusal")
	}

	if err := RestoreScoped(ctx, target, "latest", dst, true); err != nil {
		t.Fatal(err)
	}
	if got := projectNames(t, dst); !slices.Equal(got, []string{"alpha", "gamma"}) {
		t.Fatalf("restored projects = %v", got)
	}
	checkSearch(t, dst) // alpha is searchable again

	rs, err := store.Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var units, hist, files int
	rs.R.QueryRowContext(ctx, "SELECT count(*) FROM units").Scan(&units)
	rs.R.QueryRowContext(ctx, "SELECT count(*) FROM history").Scan(&hist)
	rs.R.QueryRowContext(ctx, "SELECT count(*) FROM files").Scan(&files)
	if units != 2 || files != 2 {
		t.Errorf("units %d files %d, want 2 and 2 (beta must be gone)", units, files)
	}
	// Nothing of the excluded project survives in the file, not even its text.
	raw, _ := os.ReadFile(dst)
	if strings.Contains(string(raw), "Секретный") {
		t.Error("excluded project text is still in the snapshot file")
	}
	// The marker is gone after restore, so the next snapshot of it is whole again.
	if v, _ := rs.Meta(ctx, "backup_partial"); v != "" {
		t.Errorf("partial marker kept after restore: %q", v)
	}
	// A normal backup is whole and restores without ceremony.
	if _, err := RunScoped(ctx, st.Path, []Target{LocalDir(t.TempDir())}, 5, []string{"nothing", "matches"}, nil); err == nil {
		t.Error("a project list that matches nothing must fail, not back up an empty database")
	}
}

// An empty list means every project, as before.
func TestScopedSnapshotAll(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "alpha")
	defer st.Close()
	addUnit(t, st, "alpha", "Привет, мир")
	target := LocalDir(t.TempDir())
	if _, err := RunScoped(ctx, st.Path, []Target{target}, 5, nil, nil); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "r.db")
	if err := Restore(ctx, target, "latest", dst); err != nil {
		t.Fatal(err)
	}
}
