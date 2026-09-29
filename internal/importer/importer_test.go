package importer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/exporter"
	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

func tx(s string) model.Piece { return model.Piece{Text: s} }
func cd(s string) model.Piece { return model.Piece{Text: s, Code: true} }

func writeXLIFF(t *testing.T, dir, rel string, units ...xliff.Unit) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := xliff.Write(fh, &xliff.File{Original: rel, SourceLang: "ru", TargetLang: "be", Units: units}); err != nil {
		t.Fatal(err)
	}
}

func mtUnit(id, src, tgt string) xliff.Unit {
	return xliff.Unit{ID: id, Source: []model.Piece{tx(src)}, Target: []model.Piece{tx(tgt)}, State: "needs-review-translation"}
}

func setup(t *testing.T) (context.Context, *store.Store, *store.Project, *qa.Runner) {
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
	return ctx, st, p, qa.NewRunner(p.Settings.QA)
}

func unitByKey(t *testing.T, ctx context.Context, st *store.Store, pid int64, key string) *store.Unit {
	t.Helper()
	rows, _, err := st.List(ctx, store.Filter{ProjectID: pid, Query: key, In: "key"}, store.SortFile, nil, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("unit %s: %v %v", key, rows, err)
	}
	u, err := st.Unit(ctx, rows[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestImportMergeRules(t *testing.T) {
	ctx, st, p, runner := setup(t)
	dir := t.TempDir()
	writeXLIFF(t, dir, "a/one.xlf",
		mtUnit("k1", "Привет", "Прывітанне"),
		mtUnit("k2", "Пока", "Бывай"),
		mtUnit("k3", "Нож", "Нож"),
		xliff.Unit{ID: "k4", Source: []model.Piece{tx("Беги "), cd("{KEY}")}, Target: []model.Piece{tx("Бяжы "), cd("{KEY}")}, State: "needs-review-translation"},
	)
	writeXLIFF(t, dir, "b/two.xlf", mtUnit("z", "Да", "Так"))
	stats, err := Import(ctx, st, runner, Options{ProjectID: p.ID, Root: dir})
	if err != nil || stats.Inserted != 5 {
		t.Fatalf("import: %+v %v", stats, err)
	}

	// Human edits.
	k1 := unitByKey(t, ctx, st, p.ID, "k1")
	s := "Вітаю"
	if _, err := st.SaveUnit(ctx, k1.ID, store.Save{Target: &s, State: model.Approved, Origin: "edit"}, runner); err != nil {
		t.Fatal(err)
	}
	k4 := unitByKey(t, ctx, st, p.ID, "k4")
	bad := "Бяжы"
	var mm *model.CodeMismatch
	if _, err := st.SaveUnit(ctx, k4.ID, store.Save{Target: &bad, State: model.Translated, Origin: "edit"}, runner); !errors.As(err, &mm) {
		t.Fatalf("placeholder loss accepted: %v", err)
	}

	// New run: k1 source changed (human → kept, demoted), k2 source changed
	// (MT → replaced), k3 gone (obsolete), k5 new, k4 unchanged with a new MT.
	writeXLIFF(t, dir, "a/one.xlf",
		mtUnit("k1", "Привет!", "Прывітанне!"),
		mtUnit("k2", "Пока!", "Бывай!"),
		xliff.Unit{ID: "k4", Source: []model.Piece{tx("Беги "), cd("{KEY}")}, Target: []model.Piece{tx("Уцякай "), cd("{KEY}")}, State: "needs-review-translation"},
		mtUnit("k5", "Новое", "Новае"),
	)
	stats, err = Import(ctx, st, runner, Options{ProjectID: p.ID, Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inserted != 1 || stats.SourceChanged != 2 || stats.Obsolete != 1 || stats.Updated != 3 {
		t.Errorf("stats = %+v", stats)
	}

	k1 = unitByKey(t, ctx, st, p.ID, "k1")
	if model.Plain(k1.Target) != "Вітаю" || k1.State != model.Translated || !k1.Changed {
		t.Errorf("k1 = %q %v changed=%v", model.Plain(k1.Target), k1.State, k1.Changed)
	}
	k2 := unitByKey(t, ctx, st, p.ID, "k2")
	if model.Plain(k2.Target) != "Бывай!" || k2.State != model.MT {
		t.Errorf("k2 = %q %v", model.Plain(k2.Target), k2.State)
	}
	if k4 := unitByKey(t, ctx, st, p.ID, "k4"); model.Plain(k4.Target) != "Уцякай {KEY}" {
		t.Errorf("k4 = %q", model.Plain(k4.Target))
	}
	if n, _ := st.Count(ctx, store.Filter{ProjectID: p.ID, Query: "k3", In: "key"}); n != 0 {
		t.Error("obsolete k3 still listed")
	}

	// Triggers must have kept the counters exactly where a full recount puts them.
	before, _ := st.Project(ctx, p.ID)
	if err := st.Write(ctx, func(tx *sql.Tx) error { return store.RecountFiles(ctx, tx, p.ID) }); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Project(ctx, p.ID)
	if before.Counts != after.Counts {
		t.Errorf("trigger counters %+v != recount %+v", before.Counts, after.Counts)
	}
	if after.Counts.Total != 5 || after.Counts.Translated != 1 || after.Counts.MT != 4 {
		t.Errorf("counts = %+v", after.Counts)
	}

	// Round trip through the exporter keeps the edit and the state.
	out := t.TempDir()
	if _, err := exporter.Export(ctx, st, exporter.Options{ProjectID: p.ID, Out: out, MinState: model.MT}); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(filepath.Join(out, "a", "one.xlf"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	f, _, err := xliff.Read(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Units) != 4 || model.Plain(f.Units[0].Target) != "Вітаю" || f.Units[0].State != "translated" {
		t.Errorf("exported = %+v", f.Units)
	}
}

func TestSearchAndFilters(t *testing.T) {
	ctx, st, p, runner := setup(t)
	dir := t.TempDir()
	writeXLIFF(t, dir, "one.xlf",
		mtUnit("1", "Орбита башня", "Орбіта вежа"),
		mtUnit("2", "Техпром", "Тэхпрам"),
		xliff.Unit{ID: "3", Source: []model.Piece{tx("Не переведено")}},
		mtUnit("4", "Да ", "Так"), // whitespace warning
	)
	if _, err := Import(ctx, st, runner, Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		f    store.Filter
		want int64
	}{
		{"all", store.Filter{}, 4},
		{"fts case-insensitive", store.Filter{Query: "ОРБИТА"}, 1},
		{"fts target only", store.Filter{Query: "вежа", In: "target"}, 1},
		{"fts source miss", store.Filter{Query: "вежа", In: "source"}, 0},
		{"short query", store.Filter{Query: "Да"}, 1},
		{"untranslated", store.Filter{States: []model.State{model.Untranslated}}, 1},
		{"todo", store.Filter{States: []model.State{model.Untranslated, model.MT}}, 4},
		{"any qa", store.Filter{QA: qa.Check(^uint32(0))}, 1},
		{"qa whitespace", store.Filter{QA: qa.Whitespace}, 1},
		{"qa errors", store.Filter{Errors: true}, 0},
	}
	for _, c := range cases {
		c.f.ProjectID = p.ID
		n, err := st.Count(ctx, c.f)
		if err != nil || n != c.want {
			t.Errorf("%s: count %d, want %d (%v)", c.name, n, c.want, err)
		}
		rows, _, err := st.List(ctx, c.f, store.SortFile, nil, 100)
		if err != nil || int64(len(rows)) != c.want {
			t.Errorf("%s: list %d, want %d (%v)", c.name, len(rows), c.want, err)
		}
	}

	// Keyset paging visits every row once, in each order.
	page := func(sort store.Sort) []string {
		var seen []string
		var cur *store.Cursor
		for {
			rows, next, err := st.List(ctx, store.Filter{ProjectID: p.ID}, sort, cur, 3)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				seen = append(seen, r.Key)
			}
			if next == nil {
				return seen
			}
			cur = next
		}
	}
	for sort, want := range map[store.Sort]string{
		store.SortFile:  "1 2 3 4",
		store.SortShort: "4 2 1 3", // «Да », «Техпром», «Орбита башня», «Не переведено» (id breaks ties)
		store.SortLong:  "3 1 2 4",
	} {
		if got := strings.Join(page(sort), " "); got != want {
			t.Errorf("sort %q: %s, want %s", sort, got, want)
		}
	}
	// Recently changed: an edit moves a unit to the top.
	u2 := unitByKey(t, ctx, st, p.ID, "2")
	edited := "Тэхпрам!"
	time.Sleep(1100 * time.Millisecond) // updated_at has second resolution
	if _, err := st.SaveUnit(ctx, u2.ID, store.Save{Target: &edited, State: model.Translated, Origin: "edit"}, runner); err != nil {
		t.Fatal(err)
	}
	if got := page(store.SortUpdated); got[0] != "2" || len(got) != 4 {
		t.Errorf("sort updated: %v", got)
	}

	// Bulk approve skips nothing here (no errors) and records history.
	n, err := st.SetState(ctx, store.Filter{ProjectID: p.ID}, model.Approved, true)
	if err != nil || n != 3 {
		t.Fatalf("bulk approve: %d %v", n, err)
	}
	u := unitByKey(t, ctx, st, p.ID, "2")
	if u.State != model.Approved || len(u.History) != 3 || u.History[0].Origin != "bulk" || u.History[1].Origin != "edit" {
		t.Errorf("after bulk: %v %+v", u.State, u.History)
	}
}
