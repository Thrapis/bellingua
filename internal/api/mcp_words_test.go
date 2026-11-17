package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func wordUnits() []sweepUnit {
	return []sweepUnit{
		{"w1", "a", "Ганна прыйшла. Ганна ішла."},
		{"w2", "b", "Аб'ект і аб’ект — то-бок так"},
		{"w3", "Нажмите " + keyPH, "Ганна " + keyPH + " тут"},
		{"w4", "d", ""},
		{"w5", "e", "ганна тут"},
	}
}

func words(t *testing.T, cs *mcp.ClientSession, p int64, args map[string]any) mcpWords {
	t.Helper()
	args["project"] = p
	var out mcpWords
	if text, isErr := call(t, cs, "list_words", args, &out); isErr {
		t.Fatalf("list_words %v: %s", args, text)
	}
	return out
}

func counts(out mcpWords) map[string]int {
	m := map[string]int{}
	for _, w := range out.Words {
		m[w.Word] = w.Count
	}
	return m
}

// list_words is the vocabulary of the translations: each distinct word once
// with how often it occurs, for hunting typos in a ranked list.
func TestMCPListWords(t *testing.T) {
	cs, _, p, _ := newSweep(t, wordUnits())

	out := words(t, cs, p.ID, map[string]any{})
	want := map[string]int{
		"ганна": 4, "прыйшла": 1, "ішла": 1, "тут": 2, "і": 1, "так": 1,
		"аб'ект": 1, "аб’ект": 1, // the two apostrophes are different spellings
		"то-бок": 1, // a hyphen joins a word; the dash does not
	}
	got := counts(out)
	for w, n := range want {
		if got[w] != n {
			t.Errorf("%q = %d, want %d", w, got[w], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("words = %v (the code, the dash and the untranslated unit must add none)", got)
	}
	if out.Total != len(want) || out.Units != 4 {
		t.Errorf("total=%d units=%d", out.Total, out.Units)
	}
	// Most frequent first, ties alphabetical.
	for i := 1; i < len(out.Words); i++ {
		a, b := out.Words[i-1], out.Words[i]
		if a.Count < b.Count || (a.Count == b.Count && a.Word > b.Word) {
			t.Fatalf("not ranked at %d: %+v then %+v", i, a, b)
		}
	}

	if got := counts(words(t, cs, p.ID, map[string]any{"minCount": 2})); len(got) != 2 || got["ганна"] != 4 || got["тут"] != 2 {
		t.Errorf("minCount 2 = %v", got)
	}
	if got := counts(words(t, cs, p.ID, map[string]any{"maxCount": 1})); got["ганна"] != 0 || got["прыйшла"] != 1 || len(got) != 7 {
		t.Errorf("maxCount 1 = %v", got)
	}

	// Letter case: folded by default, kept on request.
	cased := counts(words(t, cs, p.ID, map[string]any{"caseSensitive": true}))
	if cased["Ганна"] != 3 || cased["ганна"] != 1 || cased["Аб'ект"] != 1 {
		t.Errorf("caseSensitive = %v", cased)
	}

	alpha := words(t, cs, p.ID, map[string]any{"sort": "alpha"})
	if !slices.IsSortedFunc(alpha.Words, func(a, b mcpWord) int { return strings.Compare(a.Word, b.Word) }) {
		t.Errorf("alpha order = %+v", alpha.Words)
	}

	page := words(t, cs, p.ID, map[string]any{"limit": 2, "offset": 1})
	if len(page.Words) != 2 || page.Total != len(want) || page.Words[0] != out.Words[1] || page.Words[1] != out.Words[2] {
		t.Errorf("page = %+v (total %d)", page.Words, page.Total)
	}

	// The same filter as list_units chooses the units; field picks the text.
	if got := counts(words(t, cs, p.ID, map[string]any{"query": "прыйшла", "in": "target"})); len(got) != 3 || got["ганна"] != 2 {
		t.Errorf("query = %v", got)
	}
	if got := counts(words(t, cs, p.ID, map[string]any{"states": []string{"translated"}})); len(got) != 0 {
		t.Errorf("states = %v", got)
	}
	if got := counts(words(t, cs, p.ID, map[string]any{"field": "source"})); got["нажмите"] != 1 || got["{key}"] != 0 || len(got) != 5 {
		t.Errorf("source words = %v", got)
	}

	for name, a := range map[string]map[string]any{
		"bad field":  {"field": "key"},
		"bad sort":   {"sort": "random"},
		"bad limit":  {"limit": 5000},
		"bad offset": {"offset": -1},
	} {
		a["project"] = p.ID
		if text, isErr := call(t, cs, "list_words", a, nil); !isErr {
			t.Errorf("%s accepted: %s", name, text)
		}
	}
}

// skipNumbers and script cut the junk out of a typo hunt: 00012 and 2024 are
// words to the counter, but never candidates, and a Latin look-alike inside a
// Cyrillic word is exactly what "mixed" finds.
func TestMCPListWordsScript(t *testing.T) {
	cs, _, p, _ := newSweep(t, []sweepUnit{
		{"s1", "a", "Ганна 00012 hello 2024 Gаnna 3d 1990-я"}, // Gаnna: Latin G and n around a Cyrillic а
		{"s2", "b", "Ганна 00012 world"},
	})
	got := func(args map[string]any) []string {
		var ws []string
		for _, w := range words(t, cs, p.ID, args).Words {
			ws = append(ws, w.Word)
		}
		slices.Sort(ws)
		return ws
	}
	for name, c := range map[string]struct {
		args map[string]any
		want []string
	}{
		"all":                     {map[string]any{}, []string{"00012", "1990-я", "2024", "3d", "gаnna", "ганна", "hello", "world"}},
		"skipNumbers":             {map[string]any{"skipNumbers": true}, []string{"1990-я", "3d", "gаnna", "ганна", "hello", "world"}},
		"cyrillic":                {map[string]any{"script": "cyrillic"}, []string{"1990-я", "gаnna", "ганна"}},
		"noCyrillic":              {map[string]any{"script": "noCyrillic"}, []string{"00012", "2024", "3d", "hello", "world"}},
		"mixed":                   {map[string]any{"script": "mixed"}, []string{"gаnna"}},
		"no cyrillic, no numbers": {map[string]any{"script": "noCyrillic", "skipNumbers": true}, []string{"3d", "hello", "world"}},
		"with maxCount":           {map[string]any{"skipNumbers": true, "script": "cyrillic", "maxCount": 1}, []string{"1990-я", "gаnna"}},
	} {
		slices.Sort(c.want)
		if g := got(c.args); !slices.Equal(g, c.want) {
			t.Errorf("%s: %v, want %v", name, g, c.want)
		}
	}
	// total counts what is left after the filters, before paging.
	if out := words(t, cs, p.ID, map[string]any{"skipNumbers": true, "limit": 2}); out.Total != 6 || len(out.Words) != 2 {
		t.Errorf("total = %d, words = %d", out.Total, len(out.Words))
	}
	if text, isErr := call(t, cs, "list_words", map[string]any{"project": p.ID, "script": "greek"}, nil); !isErr {
		t.Errorf("bad script accepted: %s", text)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]int64{
		"90m":                  now.Add(-90 * time.Minute).Unix(),
		"48h":                  now.Add(-48 * time.Hour).Unix(),
		"1700000000":           1700000000,
		"2026-11-17":           time.Date(2026, 11, 17, 0, 0, 0, 0, time.Local).Unix(),
		"2026-11-17T09:30:00Z": time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC).Unix(),
		"2026-11-17 09:30":     time.Date(2026, 11, 17, 9, 30, 0, 0, time.Local).Unix(),
	} {
		if got, err := parseSince(in, now); err != nil || got != want {
			t.Errorf("parseSince(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "-5m", "12", "2026-13-01", "1h30"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) accepted", bad)
		}
	}
}

// lastOrigin and modifiedSince find exactly what an agent (or the editor)
// changed, on every tool that takes the unit filter.
func TestMCPFilterByOriginAndTime(t *testing.T) {
	cs, ts, p, ids := sweepFixture(t)
	time.Sleep(1100 * time.Millisecond) // updated_at has second resolution
	since := time.Now().Unix()

	if out, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": false}); isErr || out.Changed != 3 {
		t.Fatalf("sweep = %+v %s", out, text)
	}
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["k3"]), map[string]any{"target": "Рыпер ідзе.", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d %s", res.StatusCode, b)
	}
	sinceArg := strconv.FormatInt(since, 10)

	for name, c := range map[string]struct {
		args map[string]any
		want []string
	}{
		"ai":              {map[string]any{"lastOrigin": "ai"}, []string{"k1", "k2", "k7"}},
		"edit":            {map[string]any{"lastOrigin": "edit"}, []string{"k3", "k5"}},
		"since":           {map[string]any{"modifiedSince": sinceArg}, []string{"k1", "k2", "k3", "k7"}},
		"ai and since":    {map[string]any{"lastOrigin": "ai", "modifiedSince": sinceArg}, []string{"k1", "k2", "k7"}},
		"future":          {map[string]any{"modifiedSince": strconv.FormatInt(since+3600, 10)}, []string{}},
		"relative":        {map[string]any{"modifiedSince": "1h", "lastOrigin": "ai"}, []string{"k1", "k2", "k7"}},
		"with a query":    {map[string]any{"lastOrigin": "ai", "query": "варта", "in": "target"}, []string{"k1", "k2", "k7"}},
		"origin and none": {map[string]any{"lastOrigin": "mt"}, []string{}},
	} {
		got := listKeys(t, cs, p, c.args)
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	for _, bad := range []map[string]any{{"lastOrigin": "robot"}, {"modifiedSince": "yesterday"}} {
		bad["project"] = p.ID
		if text, isErr := call(t, cs, "list_units", bad, nil); !isErr {
			t.Errorf("%v accepted: %s", bad, text)
		}
	}
}

// export_units writes the matching units to a TSV next to the database, with
// full texts, so an agent can read a whole selection from disk at once.
func TestMCPExportUnits(t *testing.T) {
	cs, ts, p, ids := newSweep(t, []sweepUnit{
		{"e1", "Line one", "Радок адзін"},
		{"e2", "Two", "Два"},
		{"e3", "Three", ""},
	})
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["e1"]), map[string]any{"target": "Першы\nрадок\tтаб \\ канец", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d %s", res.StatusCode, b)
	}
	export := func(args map[string]any) (mcpExport, string, bool) {
		args["project"] = p.ID
		var out mcpExport
		text, isErr := call(t, cs, "export_units", args, &out)
		return out, text, isErr
	}
	out, text, isErr := export(map[string]any{"path": "all.tsv"})
	if isErr {
		t.Fatal(text)
	}
	if out.Rows != 3 || filepath.Base(filepath.Dir(out.Path)) != "exports" || filepath.Base(out.Path) != "all.tsv" || !filepath.IsAbs(out.Path) {
		t.Fatalf("export = %+v", out)
	}
	raw, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) != out.Bytes {
		t.Errorf("bytes = %d, file has %d", out.Bytes, len(raw))
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 4 || lines[0] != "id\tstate\tupdated\tpath\tkey\tsource\ttarget" {
		t.Fatalf("lines = %q", lines)
	}
	byKey := map[string][]string{}
	for _, l := range lines[1:] {
		f := strings.Split(l, "\t")
		if len(f) != 7 {
			t.Fatalf("a row has %d fields (tabs and newlines in texts must be escaped): %q", len(f), l)
		}
		byKey[f[4]] = f
	}
	if f := byKey["e1"]; f[0] != itoa(ids["e1"]) || f[1] != "translated" || f[5] != "Line one" || unescapeTSV(f[6]) != "Першы\nрадок\tтаб \\ канец" {
		t.Errorf("e1 = %q", f)
	}
	if f := byKey["e3"]; f[1] != "untranslated" || f[6] != "" {
		t.Errorf("e3 = %q", f)
	}
	if f := byKey["e2"]; f[2] == "" || f[3] != "one.xlf" {
		t.Errorf("e2 = %q", f)
	}

	// The same filter as list_units; an existing file is replaced.
	out, _, _ = export(map[string]any{"path": "all.tsv", "lastOrigin": "edit"})
	if out.Rows != 1 {
		t.Errorf("filtered export = %+v", out)
	}
	if raw, _ := os.ReadFile(out.Path); strings.Count(string(raw), "\n") != 2 {
		t.Errorf("file not replaced: %q", raw)
	}
	if out, _, _ = export(map[string]any{"path": "none.txt", "query": "нічога няма", "in": "target"}); out.Rows != 0 {
		t.Errorf("empty export = %+v", out)
	}

	for _, bad := range []string{"", "../x.tsv", "a/b.tsv", `a\b.tsv`, "C:x.tsv", ".hidden.tsv", "noext", "x.exe", "x.tsv.exe", strings.Repeat("a", 200) + ".tsv"} {
		if _, text, isErr := export(map[string]any{"path": bad}); !isErr {
			t.Errorf("path %q accepted: %s", bad, text)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(out.Path)); len(entries) != 2 {
		t.Errorf("exports dir holds %d entries, want 2 (no temp files left)", len(entries))
	}
}

func unescapeTSV(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// Several pairs need no query: the units that contain any find text are
// picked for them (plain text stays plain even when the pairs are found by
// pattern), and a plain notQuery stays plain too.
func TestMCPReplaceManyPairsNoQuery(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	pairs := []any{pair("Рыпер", "A"), pair("Сеткавы дазор", "B")}
	run := func(extra map[string]any) mcpSweep {
		a := map[string]any{"pairs": pairs}
		for k, v := range extra {
			a[k] = v
		}
		out, text, isErr := sweep(t, cs, p, a)
		if isErr {
			t.Fatal(text)
		}
		return out
	}
	out := run(nil) // k1, k2, k3 (Рыпер) and k7; k5 is approved, k4 has only lower case
	if out.Matched != 4 || out.Changed != 4 {
		t.Errorf("matched=%d changed=%d (%+v)", out.Matched, out.Changed, out.Samples)
	}
	if s, _ := sampleOf(out, ids["k3"]); s.After != "A ідзе. рыпер стаіць." {
		t.Errorf("k3 = %q", s.After)
	}
	if out = run(map[string]any{"ignoreCase": true}); out.Matched != 5 {
		t.Errorf("ignoreCase matched %d, want 5 (k4 has рыпер)", out.Matched)
	}
	if out = run(map[string]any{"notQuery": "ужо", "notIn": "target"}); out.Matched != 3 {
		t.Errorf("notQuery matched %d, want 3 (k7 left out)", out.Matched)
	}
	// A find text with pattern characters is still just text.
	out, _, _ = sweep(t, cs, p, map[string]any{"pairs": []any{pair("а.б", "X"), pair("(ц", "Y")}})
	if out.Matched != 0 {
		t.Errorf("pattern characters matched: %+v", out)
	}
	// Pairs that are patterns work too, and several can match one unit.
	out = run(map[string]any{"regex": true, "pairs": []any{pair(`^Рыпер`, "A"), pair(`дазор$`, "B")}})
	if out.Matched != 1 && out.Matched != 2 {
		t.Errorf("regex pairs matched %d", out.Matched)
	}
}

// A literal backslash-n in a find text never matches a line break: say so,
// instead of leaving "0 matches" to be explained by the agent.
func TestMCPReplaceBackslashWarning(t *testing.T) {
	cs, ts, p, ids := newSweep(t, []sweepUnit{{"n1", "Line", "Першы радок"}})
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["n1"]), map[string]any{"target": "Першы\nрадок", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d %s", res.StatusCode, b)
	}
	// The JSON string "Першы\\nрадок" is backslash + n.
	out, _, _ := sweep(t, cs, p, map[string]any{"query": "радок", "pairs": []any{pair(`Першы\nрадок`, "X")}})
	if out.Matched != 0 || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], `\n`) || !strings.Contains(out.Warnings[0], "line break") {
		t.Errorf("backslash-n: %+v", out)
	}
	// A real line break matches, and a pattern's \n is a line break: no warning.
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "радок", "pairs": []any{pair("Першы\nрадок", "X")}})
	if out.Matched != 1 || len(out.Warnings) != 0 {
		t.Errorf("real line break: %+v", out)
	}
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "радок", "regex": true, "pairs": []any{pair(`Першы\nрадок`, "X")}})
	if out.Matched != 1 || len(out.Warnings) != 0 {
		t.Errorf("regex \\n: %+v", out)
	}
	// No warning when the pair did match something.
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "радок", "pairs": []any{pair(`радок`, "X")}})
	if len(out.Warnings) != 0 {
		t.Errorf("unneeded warning: %+v", out)
	}
}

// list_units rows are previews; full=true returns whole texts for a page.
func TestMCPListUnitsFull(t *testing.T) {
	long := strings.Repeat("Слова ", 200) // 1200 runes
	cs, _, p, _ := newSweep(t, []sweepUnit{{"f1", long, long + "канец"}, {"f2", "Кароткі", "Кароткі"}})
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	if len(page.Units) != 2 || !page.Units[0].Truncated || len([]rune(page.Units[0].Source)) >= len([]rune(long)) {
		t.Fatalf("preview page = %+v", page.Units)
	}
	page = mcpPage{} // decoding into old rows would keep their omitted fields
	call(t, cs, "list_units", map[string]any{"project": p.ID, "full": true}, &page)
	if len(page.Units) != 2 || page.Units[0].Truncated || page.Units[0].Source != long {
		t.Fatalf("full page = %+v", page.Units[0])
	}
	if got := derefStr(page.Units[0].Target); !strings.HasSuffix(got, "канец") || len(got) <= len(long) {
		t.Errorf("full target is cut: %d bytes", len(got))
	}
	// A full page is smaller, so whole texts never flood the reply.
	many := make([]sweepUnit, 60)
	for i := range many {
		many[i] = sweepUnit{"m" + strconv.Itoa(i), "s", "t"}
	}
	cs2, _, p2, _ := newSweep(t, many)
	page = mcpPage{}
	call(t, cs2, "list_units", map[string]any{"project": p2.ID, "full": true, "limit": 200}, &page)
	if len(page.Units) != 50 || page.Next == "" {
		t.Errorf("full page size = %d next=%q, want 50", len(page.Units), page.Next)
	}
}
