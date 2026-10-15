package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

const histXLF = `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>
<trans-unit id="1"><source>Да</source><target state="needs-review-translation">%s</target></trans-unit>
<trans-unit id="2"><source>Нет</source></trans-unit>
<trans-unit id="3"><source>Ok</source></trans-unit>
</body></file></xliff>`

func histFixture(t *testing.T) (*store.Store, *store.Project, *qa.Runner, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.CreateProject(ctx, "p", "ru", "be")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeXLF(t, dir, "Так")
	runner := qa.NewRunner(p.Settings.QA)
	if _, err := importer.Import(ctx, st, runner, importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	return st, p, runner, dir
}

func writeXLF(t *testing.T, dir, unit1 string) {
	t.Helper()
	body := []byte(fmt.Sprintf(histXLF, unit1))
	if err := os.WriteFile(filepath.Join(dir, "x.xlf"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func unitID(t *testing.T, st *store.Store, pid int64, key string) int64 {
	t.Helper()
	var id int64
	if err := st.R.QueryRow(`SELECT id FROM units WHERE project_id = ? AND key = ?`, pid, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Every writer records what it replaced; ai and mt stay separate origins, and
// a re-import records only units whose target or state really changed.
func TestHistoryFeed(t *testing.T) {
	ctx := context.Background()
	st, p, runner, dir := histFixture(t)
	tr := func(s string) *string { return &s }

	u2, u3 := unitID(t, st, p.ID, "2"), unitID(t, st, p.ID, "3")
	if _, err := st.SaveUnit(ctx, u2, store.Save{Target: tr("Не"), State: model.MT, Origin: "mt", Batch: "mt-1"}, runner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveUnit(ctx, u3, store.Save{Target: tr("Добра"), State: model.Translated, Origin: "ai"}, runner); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetState(ctx, store.Filter{ProjectID: p.ID, States: []model.State{model.Translated}}, model.Approved, false, "bulk-1"); err != nil {
		t.Fatal(err)
	}
	// Re-import: unit 1 is still machine-translated, so the new incoming text replaces it.
	writeXLF(t, dir, "Ага")
	if _, err := importer.Import(ctx, st, runner, importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	// A second, identical re-import changes nothing and records nothing.
	if _, err := importer.Import(ctx, st, runner, importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}

	days, next, err := st.HistoryDays(ctx, p.ID, 0, 0, 7)
	if err != nil || next != 0 || len(days) != 1 {
		t.Fatalf("days = %+v next=%d err=%v", days, next, err)
	}
	got := map[string]int64{}
	for _, b := range days[0].Batches {
		got[b.Origin] += b.Count
	}
	want := map[string]int64{"mt": 1, "ai": 1, "bulk": 1, "import": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("origin %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("origins = %v", got)
	}

	rows, next2, err := st.HistoryList(ctx, store.HistoryFilter{ProjectID: p.ID, Origins: []string{"ai"}}, nil, 10)
	if err != nil || next2 != nil || len(rows) != 1 {
		t.Fatalf("ai rows = %+v %v", rows, err)
	}
	r := rows[0]
	if r.Key != "3" || r.OldState != model.Untranslated || r.State != model.Translated || r.Old != nil || r.New == nil || *r.New != "Добра" {
		t.Errorf("ai row = %+v", r)
	}
	rows, _, _ = st.HistoryList(ctx, store.HistoryFilter{ProjectID: p.ID, Batch: "mt-1"}, nil, 10)
	if len(rows) != 1 || rows[0].Origin != "mt" {
		t.Errorf("batch rows = %+v", rows)
	}
	rows, _, _ = st.HistoryList(ctx, store.HistoryFilter{ProjectID: p.ID, Origins: []string{"bulk"}}, nil, 10)
	if len(rows) != 1 || rows[0].OldState != model.Translated || rows[0].State != model.Approved || rows[0].Batch != "bulk-1" {
		t.Errorf("bulk rows = %+v", rows)
	}

	// Paging: one row per page walks the whole feed once.
	var seen int
	var cur *store.Cursor
	for {
		page, n, err := st.HistoryList(ctx, store.HistoryFilter{ProjectID: p.ID}, cur, 1)
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page)
		if n == nil {
			break
		}
		cur = n
	}
	if seen != 4 {
		t.Errorf("paged %d rows, want 4", seen)
	}

	// Deleting the project removes its history.
	if err := st.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	st.R.QueryRow(`SELECT count(*) FROM history`).Scan(&n)
	if n != 0 {
		t.Errorf("%d history rows left", n)
	}
}
