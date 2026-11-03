package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/store"
)

// The reference-language search index is stripped from the snapshot and
// rebuilt on restore, like the main one.
func TestRestoreRebuildsReferenceIndex(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	addUnit(t, st, "first", "Привет, мир")
	p, err := st.ProjectByName(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	en := t.TempDir()
	body := `<?xml version="1.0"?><xliff version="1.2"><file original="a" source-language="ru" target-language="en"><body>` +
		`<trans-unit id="1"><source>Привет, мир</source><target>Hello, world</target></trans-unit></body></file></xliff>`
	if err := os.WriteFile(filepath.Join(en, "a.xlf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.ImportLang(ctx, st, importer.LangOptions{ProjectID: p.ID, Root: en}); err != nil {
		t.Fatal(err)
	}

	target := LocalDir(t.TempDir())
	if _, err := Run(ctx, st.Path, []Target{target}, 1); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, target, "latest", dst); err != nil {
		t.Fatal(err)
	}
	r, err := store.Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n, err := r.Count(ctx, store.Filter{ProjectID: p.ID, Query: "HELLO", In: "lang:en"})
	if err != nil || n != 1 {
		t.Errorf("reference search after restore: %d %v", n, err)
	}
}

// A snapshot carries no reference index at all (a plain FTS5 table keeps its
// data after DELETE, which made backups ~70 MB bigger), and restore recreates it.
func TestSnapshotDropsReferenceIndex(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	cp := filepath.Join(t.TempDir(), "snap.db")
	if _, err := st.W.ExecContext(ctx, "VACUUM INTO '"+filepath.ToSlash(cp)+"'"); err != nil {
		t.Fatal(err)
	}
	if err := stripIndex(ctx, cp); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name LIKE 'unit_langs_fts%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("reference index objects left in the snapshot: %d %v", n, err)
	}
	if err := rebuildIndex(ctx, cp); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'unit_langs_fts'").Scan(&n); err != nil || n != 1 {
		t.Errorf("reference index not recreated: %d %v", n, err)
	}
}
