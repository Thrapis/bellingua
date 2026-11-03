package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

// searchFixture imports four units and an English reference text for three.
func searchFixture(t *testing.T) (st *store.Store, p *store.Project, runner *qa.Runner, keys func(store.Filter, store.Sort) string) {
	t.Helper()
	ctx, st, p, runner := setup(t)
	main := t.TempDir()
	writeXLIFF(t, main, "a.xlf",
		mtUnit("1", "Орбита башня", "Орбіта вежа"),
		mtUnit("2", "Техпром", "Тэхпрам"),
		xliff.Unit{ID: "3", Source: []model.Piece{tx("Не переведено")}},
		mtUnit("4", "Да ", "Так"),
	)
	if _, err := Import(ctx, st, runner, Options{ProjectID: p.ID, Root: main}); err != nil {
		t.Fatal(err)
	}
	en := t.TempDir()
	writeLangXLIFF(t, en, "a.xlf", "en-US",
		refUnit("1", []model.Piece{tx("Орбита башня")}, []model.Piece{tx("Mr. Stud's orbital tower")}),
		refUnit("2", []model.Piece{tx("Техпром")}, []model.Piece{tx("Techprom")}),
		refUnit("4", []model.Piece{tx("Да ")}, []model.Piece{tx("Yes")}),
	)
	if _, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: en}); err != nil {
		t.Fatal(err)
	}
	keys = func(f store.Filter, sort store.Sort) string {
		t.Helper()
		f.ProjectID = p.ID
		var seen []string
		var cur *store.Cursor
		for {
			rows, next, err := st.List(ctx, f, sort, cur, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				seen = append(seen, r.Key)
			}
			if next == nil {
				return strings.Join(seen, " ")
			}
			cur = next
		}
	}
	return st, p, runner, keys
}

// Search fields combine with OR, and "lang:xx" searches a reference language.
func TestSearchFields(t *testing.T) {
	st, p, _, keys := searchFixture(t)
	ctx := t.Context()
	for _, c := range []struct {
		name string
		f    store.Filter
		want string
	}{
		{"reference trigram, case-insensitive", store.Filter{Query: "MR. STUD", In: "lang:en-US"}, "1"},
		{"reference short query", store.Filter{Query: "Ye", In: "lang:en-US"}, "4"},
		{"reference miss", store.Filter{Query: "Орбита", In: "lang:en-US"}, ""},
		{"unknown language matches nothing", store.Filter{Query: "Techprom", In: "lang:de"}, ""},
		{"source or reference", store.Filter{Query: "Techprom", In: "source,lang:en-US"}, "2"},
		{"source or reference, both hit", store.Filter{Query: "орб", In: "source,lang:en-US"}, "1"},
		{"target or key", store.Filter{Query: "3", In: "target,key"}, "3"},
		{"source+target spelled out", store.Filter{Query: "вежа", In: "source,target"}, "1"},
		{"both keyword", store.Filter{Query: "вежа", In: "both"}, "1"},
		{"reference plus filter", store.Filter{Query: "o", In: "lang:en-US", States: []model.State{model.Untranslated}}, ""},
	} {
		if got := keys(c.f, store.SortFile); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		c.f.ProjectID = p.ID
		n, err := st.Count(ctx, c.f)
		if want := int64(len(strings.Fields(c.want))); err != nil || n != want {
			t.Errorf("%s: count %d, want %d (%v)", c.name, n, want, err)
		}
	}

	// A re-import replaces the reference index; deleting the language empties it.
	en := t.TempDir()
	writeLangXLIFF(t, en, "a.xlf", "en-US",
		refUnit("2", []model.Piece{tx("Техпром")}, []model.Piece{tx("Technoprom")}),
	)
	if _, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: en}); err != nil {
		t.Fatal(err)
	}
	if got := keys(store.Filter{Query: "Mr. Stud", In: "lang:en-US"}, store.SortFile); got != "" {
		t.Errorf("after re-import the old text is still found: %q", got)
	}
	if got := keys(store.Filter{Query: "Technoprom", In: "lang:en-US"}, store.SortFile); got != "2" {
		t.Errorf("after re-import: %q", got)
	}
	if err := st.DeleteProjectLang(ctx, p.ID, "en-US"); err != nil {
		t.Fatal(err)
	}
	if got := keys(store.Filter{Query: "Technoprom", In: "lang:en-US"}, store.SortFile); got != "" {
		t.Errorf("after delete: %q", got)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, "SELECT count(*) FROM unit_langs_fts").Scan(&n); err != nil || n != 0 {
		t.Errorf("unit_langs_fts rows after delete: %d %v", n, err)
	}
}

// The new orders page by keyset through every row once.
func TestNewSorts(t *testing.T) {
	st, p, runner, keys := searchFixture(t)
	ctx := t.Context()

	// Translations: Орбіта вежа (11), Тэхпрам (7), Так (3); unit 3 has none.
	for sort, want := range map[store.Sort]string{
		store.SortTargetShort: "4 2 1",
		store.SortTargetLong:  "1 2 4",
		store.SortStatus:      "3 1 2 4", // untranslated first, then machine in file order
	} {
		if got := keys(store.Filter{}, sort); got != want {
			t.Errorf("sort %q: %q, want %q", sort, got, want)
		}
	}

	// Oldest changed first is the reverse of recently changed; an edit moves a unit.
	u2 := unitByKey(t, ctx, st, p.ID, "2")
	time.Sleep(1100 * time.Millisecond) // updated_at has second resolution
	edited := "Тэхпрам!"
	if _, err := st.SaveUnit(ctx, u2.ID, store.Save{Target: &edited, State: model.Translated, Origin: "edit"}, runner); err != nil {
		t.Fatal(err)
	}
	if got := keys(store.Filter{}, store.SortOldest); !strings.HasSuffix(got, " 2") || len(strings.Fields(got)) != 4 {
		t.Errorf("sort oldest: %q", got)
	}
	// Status order now puts the translated unit after the machine ones.
	if got := keys(store.Filter{}, store.SortStatus); got != "3 1 4 2" {
		t.Errorf("sort status after edit: %q", got)
	}
	for _, name := range []string{"tshort", "tlong", "status", "old"} {
		if _, ok := store.ParseSort(name); !ok {
			t.Errorf("ParseSort(%q) rejected", name)
		}
	}
}
