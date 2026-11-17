package store_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

const regexXLF = `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>
<trans-unit id="1"><source>Анна пришла</source><target state="translated">Ганна прыйшла</target></trans-unit>
<trans-unit id="2"><source>Анна ушла</source><target state="translated">Ана пайшла</target></trans-unit>
<trans-unit id="3"><source>Группа К</source><target state="translated">Групы Да</target></trans-unit>
<trans-unit id="4"><source>Он шёл в лес</source><target state="translated">Ён ішоў ў лес</target></trans-unit>
<trans-unit id="5"><source>Пауза</source><target state="needs-review-translation">Паўза</target></trans-unit>
<trans-unit id="6"><source>Нет</source></trans-unit>
</body></file></xliff>`

func regexFixture(t *testing.T) (*store.Store, int64) {
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
	if err := os.WriteFile(filepath.Join(dir, "x.xlf"), []byte(regexXLF), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	return st, p.ID
}

// keys lists the matching unit keys and checks that Count agrees with List.
func keys(t *testing.T, st *store.Store, f store.Filter) []string {
	t.Helper()
	ctx := context.Background()
	rows, _, err := st.List(ctx, f, store.SortFile, nil, 100)
	if err != nil {
		t.Fatalf("List(%+v): %v", f, err)
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Key)
	}
	sort.Strings(out)
	n, err := st.Count(ctx, f)
	if err != nil || int(n) != len(out) {
		t.Errorf("Count(%+v) = %d, %v; List found %v", f, n, err, out)
	}
	return out
}

// Regex search finds what substring search cannot: a letter at a word end,
// alternatives, anchors. It is case-sensitive unless the pattern says (?i).
func TestRegexSearch(t *testing.T) {
	st, pid := regexFixture(t)
	for _, c := range []struct {
		name  string
		query string
		in    string
		want  []string
	}{
		{"letter at a word end, no trigram to index", `ў\b`, "target", []string{"4"}},
		{"a lone letter as a word", `\bў\b`, "target", []string{"4"}},
		{"alternatives with a literal for the index", `Груп(ы|а) (К|Да)`, "", []string{"3"}},
		{"case-sensitive by default", `анна`, "source", []string{}},
		{"(?i) ignores case", `(?i)анна`, "source", []string{"1", "2"}},
		{"both fields: any may match", `(?i)^ан`, "", []string{"1", "2"}},
		{"anchored key", `^[35]$`, "key", []string{"3", "5"}},
		{"untranslated units never match a target pattern", `.*`, "target", []string{"1", "2", "3", "4", "5"}},
	} {
		got := keys(t, st, store.Filter{ProjectID: pid, Query: c.query, In: c.in, Regex: true})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q in %q = %v, want %v", c.name, c.query, c.in, got, c.want)
		}
	}
	// Without Regex the same text is an ordinary substring.
	if got := keys(t, st, store.Filter{ProjectID: pid, Query: `ў\b`, In: "target"}); len(got) != 0 {
		t.Errorf("plain search treated the query as a pattern: %v", got)
	}
}

// A broken pattern is an error for the caller, not a panic or an empty list.
func TestRegexInvalid(t *testing.T) {
	st, pid := regexFixture(t)
	f := store.Filter{ProjectID: pid, Query: `(аб`, Regex: true}
	if err := f.Validate(); err == nil {
		t.Error("Validate accepted a broken pattern")
	}
	if _, _, err := st.List(context.Background(), f, store.SortFile, nil, 10); err == nil {
		t.Error("List ran a broken pattern")
	}
	if err := (store.Filter{ProjectID: pid, Query: `(аб`}).Validate(); err != nil {
		t.Errorf("a plain query is never a broken pattern: %v", err)
	}
}

// NotQuery drops the units that contain it, in its own fields: "the source
// has the name but the translation lacks it" in one query. A unit without a
// translation does not contain anything, so it stays.
func TestNotQuery(t *testing.T) {
	st, pid := regexFixture(t)
	for _, c := range []struct {
		name string
		f    store.Filter
		want []string
	}{
		{"source has it, target lacks the agreed form",
			store.Filter{Query: "Анна", In: "source", NotQuery: "Ганна", NotIn: "target"}, []string{"2"}},
		{"NotQuery alone keeps untranslated units",
			store.Filter{NotQuery: "Груп", NotIn: "target"}, []string{"1", "2", "4", "5", "6"}},
		{"short NotQuery (no trigram)",
			store.Filter{NotQuery: "ў", NotIn: "target"}, []string{"1", "2", "3", "6"}},
		{"NotIn defaults to source and target: neither may contain it",
			store.Filter{NotQuery: "Анна"}, []string{"3", "4", "5", "6"}},
		{"regex applies to both queries",
			store.Filter{Query: `^Анна`, In: "source", NotQuery: `^Ган`, NotIn: "target", Regex: true}, []string{"2"}},
		{"regex NotQuery alone",
			store.Filter{NotQuery: `ў\b`, NotIn: "target", Regex: true}, []string{"1", "2", "3", "5", "6"}},
	} {
		c.f.ProjectID = pid
		if got := keys(t, st, c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
