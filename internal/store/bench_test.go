package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
)

// Benchmarks run against a real database (e.g. a large imported
// project): BELLINGUA_BENCH_DB=path/to.db go test -bench . ./internal/store
func benchStore(b *testing.B) (*Store, int64) {
	path := os.Getenv("BELLINGUA_BENCH_DB")
	if path == "" {
		b.Skip("BELLINGUA_BENCH_DB not set")
	}
	st, err := Open(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	ps, err := st.Projects(context.Background())
	if err != nil || len(ps) == 0 {
		b.Fatalf("no project: %v", err)
	}
	return st, ps[0].ID
}

func benchList(b *testing.B, f Filter, after *Cursor) { benchListSorted(b, f, SortFile, after) }

func benchListSorted(b *testing.B, f Filter, sort Sort, after *Cursor) {
	st, pid := benchStore(b)
	f.ProjectID = pid
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := st.List(ctx, f, sort, after, 200); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListFirstPage(b *testing.B) { benchList(b, Filter{}, nil) }

// Deep in the list: keyset makes this as cheap as the first page.
func BenchmarkListDeepPage(b *testing.B) { benchList(b, Filter{}, &Cursor{A: 3000}) }

func BenchmarkSortUpdated(b *testing.B) { benchListSorted(b, Filter{}, SortUpdated, nil) }
func BenchmarkSortShort(b *testing.B)   { benchListSorted(b, Filter{}, SortShort, nil) }
func BenchmarkSortLong(b *testing.B)    { benchListSorted(b, Filter{}, SortLong, nil) }
func BenchmarkSortShortDeep(b *testing.B) {
	benchListSorted(b, Filter{}, SortShort, &Cursor{A: 60, B: 0})
}

// Sort + a selective filter: the order's index is scanned until 200 match.
func BenchmarkSortLongQA(b *testing.B) {
	benchListSorted(b, Filter{QA: qa.Check(^uint32(0))}, SortLong, nil)
}

func BenchmarkListTodo(b *testing.B) {
	benchList(b, Filter{States: []model.State{model.Untranslated, model.MT}}, nil)
}

func BenchmarkListQAIssues(b *testing.B) {
	benchList(b, Filter{QA: qa.Check(^uint32(0))}, nil)
}

func BenchmarkSearchCommon(b *testing.B) { benchList(b, Filter{Query: "что"}, nil) }
func BenchmarkSearchRare(b *testing.B)   { benchList(b, Filter{Query: "Орбита"}, nil) }
func BenchmarkSearchShort(b *testing.B)  { benchList(b, Filter{Query: "Да"}, nil) }

func BenchmarkCountAll(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.Count(ctx, Filter{ProjectID: pid}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCountQA(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.Count(ctx, Filter{ProjectID: pid, QA: qa.Check(^uint32(0))}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCountSearch(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.Count(ctx, Filter{ProjectID: pid, Query: "что"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSaveUnit(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	rows, _, err := st.List(ctx, Filter{ProjectID: pid}, SortFile, nil, 1)
	if err != nil || len(rows) == 0 {
		b.Fatal(err)
	}
	u, err := st.Unit(ctx, rows[0].ID, false)
	if err != nil {
		b.Fatal(err)
	}
	orig := model.Plain(u.Target)
	runner := qa.NewRunner(qa.DefaultConfig("be"))
	alt := [2]string{orig + " ", orig}
	b.ResetTimer()
	i := 0
	for b.Loop() {
		t := alt[i%2]
		i++
		if _, err := st.SaveUnit(ctx, u.ID, Save{Target: &t, State: u.State, Origin: "bench", Force: true}, runner); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	st.SaveUnit(ctx, u.ID, Save{Target: &orig, State: u.State, Origin: "bench", Force: true}, runner)
}

// The page cost of list_units' langs: the same first page, then one
// UnitLangsFor query for its 200 ids.
func BenchmarkListWithLangs(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		rows, _, err := st.List(ctx, Filter{ProjectID: pid}, SortFile, nil, 200)
		if err != nil {
			b.Fatal(err)
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if _, err := st.UnitLangsFor(ctx, ids, []string{"uk", "en"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnitLangsFor(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	rows, _, err := st.List(ctx, Filter{ProjectID: pid}, SortFile, &Cursor{A: 3000}, 200)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := st.UnitLangsFor(ctx, ids, []string{"uk", "en"}); err != nil {
			b.Fatal(err)
		}
	}
}

// Regex with a literal: the trigram index narrows, the pattern verifies.
func BenchmarkRegexLiteral(b *testing.B) {
	benchList(b, Filter{Query: `Орбит\p{L}*\b`, In: "source", Regex: true}, nil)
}

// Regex with no literal of 3+ characters: the pattern runs on every unit
// until 200 match (a common ending, so the scan stops early).
func BenchmarkRegexNoLiteral(b *testing.B) {
	benchList(b, Filter{Query: `ў\b`, In: "target", Regex: true}, nil)
}

// Worst case: a rare pattern without a literal scans every unit.
func BenchmarkRegexNoLiteralRare(b *testing.B) {
	benchList(b, Filter{Query: `ъ\b`, In: "source", Regex: true}, nil)
}

func BenchmarkRegexCount(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.Count(ctx, Filter{ProjectID: pid, Query: `ў\b`, In: "target", Regex: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// A name in the source, its agreed form missing from the translation.
func BenchmarkNotQuery(b *testing.B) {
	benchList(b, Filter{Query: "Орбита", In: "source", NotQuery: "Арбіта", NotIn: "target"}, nil)
}

// NotQuery alone: everything except the matches, first 200.
func BenchmarkNotQueryAlone(b *testing.B) {
	benchList(b, Filter{NotQuery: "что", NotIn: "target"}, nil)
}

// Units changed lately: units_updated serves the bound, the rest is cheap.
func BenchmarkListUpdatedSince(b *testing.B) {
	benchList(b, Filter{UpdatedSince: time.Now().Add(-24 * 365 * time.Hour).Unix()}, nil)
}

// "What the agent changed last": a history lookup per unit until 200 match.
func BenchmarkListLastOriginAI(b *testing.B) { benchList(b, Filter{LastOrigin: "ai"}, nil) }

// Worst case: an origin few units have, so the lookup runs on every unit.
func BenchmarkListLastOriginRare(b *testing.B) { benchList(b, Filter{LastOrigin: "tm"}, nil) }

func BenchmarkCountLastOriginAI(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.Count(ctx, Filter{ProjectID: pid, LastOrigin: "ai"}); err != nil {
			b.Fatal(err)
		}
	}
}

// ScanTexts over the whole project: what list_words reads.
func BenchmarkScanTexts(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	for b.Loop() {
		if _, err := st.ScanTexts(ctx, Filter{ProjectID: pid}, true, func(string) {}); err != nil {
			b.Fatal(err)
		}
	}
}
