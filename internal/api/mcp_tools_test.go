package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

// newMCPXLIFF is newMCP over a project imported from xlf (one file, one.xlf).
func newMCPXLIFF(t *testing.T, xlf string) (*mcp.ClientSession, *httptest.Server, *store.Project) {
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
	if err := os.WriteFile(filepath.Join(dir, "one.xlf"), []byte(xlf), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	bm, _ := backup.NewManager(st, config.Backup{}, log)
	ts := httptest.NewServer(New(ctx, st, config.Default(), nil, bm, nil, log).Handler())
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, ts, p
}

func xliff(units ...string) string {
	return `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>` +
		strings.Join(units, "") + `</body></file></xliff>`
}

// mtUnit is a unit with a machine translation, i.e. state mt.
func mtUnit(id, src string) string {
	return `<trans-unit id="` + id + `"><source>` + src + `</source><target state="needs-review-translation">MT ` + src + `</target></trans-unit>`
}

const (
	antonSrc = "Поговорить с Антоном о письме"
	antonTgt = "Пагаварыць з Антонам пра ліст"
)

// tmFixture: a, b, c share a source; d is one word off; e is unrelated.
func tmFixture() string {
	return xliff(mtUnit("a", antonSrc), mtUnit("b", antonSrc), mtUnit("c", antonSrc),
		mtUnit("d", "Поговорить с Олегом о письме"), mtUnit("e", "Купить пистолет"))
}

// unitIDs maps unit keys to ids by listing every unit through the tool.
func unitIDs(t *testing.T, cs *mcp.ClientSession, pid int64) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	cursor := ""
	for {
		var page mcpPage
		args := map[string]any{"project": pid, "limit": 200}
		if cursor != "" {
			args["cursor"] = cursor
		}
		call(t, cs, "list_units", args, &page)
		for _, u := range page.Units {
			ids[u.Key] = u.ID
		}
		if page.Next == "" {
			return ids
		}
		cursor = page.Next
	}
}

// waitJob polls get_job until the job leaves "running".
func waitJob(t *testing.T, cs *mcp.ClientSession, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var j map[string]any
		call(t, cs, "get_job", map[string]any{"id": id}, &j)
		if j["status"] != "running" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still running", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An agent save feeds translation memory exactly like an editor save: the
// fuzzy index sees it, identical strings are counted and can be propagated,
// and propagation stays below human work.
func TestMCPTranslationMemory(t *testing.T) {
	cs, _, p := newMCPXLIFF(t, tmFixture())
	ids := unitIDs(t, cs, p.ID)

	// Build the fuzzy index first, so the save below has to update it.
	call(t, cs, "get_unit_tm", map[string]any{"id": ids["e"]}, nil)

	var saved mcpUnit
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": ids["a"], "target": antonTgt}, &saved); isErr {
		t.Fatalf("save: %s", text)
	}
	if saved.Duplicates != 2 {
		t.Errorf("save should report 2 identical strings without a human translation, got %d", saved.Duplicates)
	}

	var tm mcpTM
	call(t, cs, "get_unit_tm", map[string]any{"id": ids["b"]}, &tm)
	if len(tm.Matches) != 1 || tm.Matches[0].Score != 100 || tm.Matches[0].Unit != ids["a"] || tm.Matches[0].Target != antonTgt {
		t.Errorf("exact match for b = %+v", tm.Matches)
	}
	if tm.Duplicates != 1 { // c; a is human now
		t.Errorf("b duplicates = %d, want 1", tm.Duplicates)
	}
	call(t, cs, "get_unit_tm", map[string]any{"id": ids["d"]}, &tm)
	if len(tm.Matches) != 1 || tm.Matches[0].Score != 80 {
		t.Errorf("fuzzy match for d (1 of 5 words differs) = %+v", tm.Matches)
	}

	var n mcpCount
	call(t, cs, "count_units", map[string]any{"project": p.ID, "same": ids["a"]}, &n)
	if n.Count != 3 {
		t.Errorf("same-source count = %d, want 3", n.Count)
	}
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID, "same": ids["a"]}, &page)
	if len(page.Units) != 3 {
		t.Errorf("same-source list = %d units, want 3", len(page.Units))
	}

	var out map[string]int
	if text, isErr := call(t, cs, "propagate_translation", map[string]any{"id": ids["a"]}, &out); isErr || out["changed"] != 2 {
		t.Errorf("propagate = %s", text)
	}
	var c mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": ids["c"], "history": true}, &c)
	if c.State != "translated" || c.Target == nil || *c.Target != antonTgt || c.History[0].Origin != "tm" {
		t.Errorf("c after propagate = %+v", c)
	}
	call(t, cs, "get_unit_tm", map[string]any{"id": ids["a"]}, &tm)
	if tm.Duplicates != 0 {
		t.Errorf("duplicates after propagate = %d", tm.Duplicates)
	}

	// Nothing to propagate from a string without a human translation.
	if text, isErr := call(t, cs, "propagate_translation", map[string]any{"id": ids["e"]}, nil); !isErr || !strings.Contains(text, "no human translation") {
		t.Errorf("propagate from mt: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "propagate_translation", map[string]any{"id": 999999}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("propagate missing: %v %s", isErr, text)
	}
}

// Reference languages come through as plain strings; a project without any
// answers with an empty list.
func TestMCPUnitLangs(t *testing.T) {
	cs, ts, p := newMCPXLIFF(t, tmFixture())
	ids := unitIDs(t, cs, p.ID)

	if text, isErr := call(t, cs, "get_unit_langs", map[string]any{"id": ids["a"]}, nil); isErr || strings.TrimSpace(text) != "[]" {
		t.Errorf("no extra language: %v %s", isErr, text)
	}

	dir := t.TempDir()
	en := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="en"><body>
<trans-unit id="a"><source>` + antonSrc + `</source><target>Talk to Anton about the letter</target></trans-unit>
</body></file></xliff>`
	os.WriteFile(filepath.Join(dir, "one.xlf"), []byte(en), 0o644)
	pid := "/api/projects/" + itoa(p.ID)
	if res, b := do(t, ts, "POST", pid+"/langs/import", map[string]any{"dir": dir}); res.StatusCode != http.StatusAccepted {
		t.Fatalf("import: %d %s", res.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, b := do(t, ts, "GET", pid+"/langs", nil); strings.Contains(string(b), `"lang":"en"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("language never listed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	var langs []mcpLang
	call(t, cs, "get_unit_langs", map[string]any{"id": ids["a"]}, &langs)
	if len(langs) != 1 || langs[0].Lang != "en" || langs[0].Target == nil || *langs[0].Target != "Talk to Anton about the letter" || langs[0].Outdated {
		t.Errorf("langs for a = %+v", langs)
	}
	call(t, cs, "get_unit_langs", map[string]any{"id": ids["e"]}, &langs)
	if len(langs) != 0 {
		t.Errorf("langs for a unit the import didn't match = %+v", langs)
	}
	if text, isErr := call(t, cs, "get_unit_langs", map[string]any{"id": 999999}, nil); isErr && !strings.Contains(text, "not found") {
		t.Errorf("missing unit: %s", text)
	}
}

// term_forms lists the inflected forms found in the project's sources.
func TestMCPTermForms(t *testing.T) {
	cs, _, p := newMCPXLIFF(t, xliff(
		`<trans-unit id="a"><source>Улица Баумана закрыта</source></trans-unit>`,
		`<trans-unit id="b"><source>Подарок Бауману</source></trans-unit>`,
		`<trans-unit id="c"><source>Совсем другое</source></trans-unit>`))
	var forms []struct {
		Form  string `json:"form"`
		Count int    `json:"count"`
	}
	if text, isErr := call(t, cs, "term_forms", map[string]any{"project": p.ID, "source": "Бауман"}, &forms); isErr {
		t.Fatalf("term_forms: %s", text)
	}
	got := map[string]int{}
	for _, f := range forms {
		got[f.Form] = f.Count
	}
	if got["Баумана"] != 1 || got["Бауману"] != 1 || len(got) > 2 {
		t.Errorf("forms = %v", got)
	}
	if text, isErr := call(t, cs, "term_forms", map[string]any{"project": p.ID, "source": "Несуществующее"}, nil); isErr || strings.TrimSpace(text) != "[]" {
		t.Errorf("no forms: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "term_forms", map[string]any{"project": p.ID, "source": "  "}, nil); !isErr || !strings.Contains(text, "source is required") {
		t.Errorf("blank source: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "term_forms", map[string]any{"project": 999999, "source": "Бауман"}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("missing project: %v %s", isErr, text)
	}
}

func TestMCPJobs(t *testing.T) {
	cs, _, p := newMCP(t)
	var job struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if text, isErr := call(t, cs, "recheck_qa", map[string]any{"project": p.ID}, &job); isErr || job.Kind != "qa" {
		t.Fatalf("recheck_qa: %s", text)
	}
	if j := waitJob(t, cs, job.ID); j["status"] != "done" {
		t.Errorf("job = %v", j)
	}
	var jobs []struct {
		ID string `json:"id"`
	}
	call(t, cs, "list_jobs", nil, &jobs)
	if !slices.ContainsFunc(jobs, func(j struct {
		ID string `json:"id"`
	}) bool {
		return j.ID == job.ID
	}) {
		t.Errorf("list_jobs = %+v, want %s", jobs, job.ID)
	}
	if text, isErr := call(t, cs, "get_job", map[string]any{"id": "nope-1"}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("unknown job: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "recheck_qa", map[string]any{"project": 999999}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("recheck of a missing project: %v %s", isErr, text)
	}
}

// Bad arguments are stopped by the tool's schema before any code runs, and
// the error names the offending field.
func TestMCPInputValidation(t *testing.T) {
	cs, _, p := newMCP(t)
	ids := unitIDs(t, cs, p.ID)
	before := func() int64 {
		var n mcpCount
		call(t, cs, "count_units", map[string]any{"project": p.ID, "states": []string{"translated"}}, &n)
		return n.Count
	}()

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string // must appear in the error
	}{
		{"id of the wrong type", "get_unit", map[string]any{"id": "abc"}, "id"},
		{"missing id", "get_unit", map[string]any{}, "id"},
		{"unknown field", "get_unit", map[string]any{"id": ids["2"], "bogus": true}, "bogus"},
		{"missing project", "list_units", map[string]any{}, "project"},
		{"missing target", "save_translation", map[string]any{"id": ids["2"]}, "target"},
		{"states of the wrong type", "count_units", map[string]any{"project": p.ID, "states": "mt"}, "states"},
		{"unknown state name", "count_units", map[string]any{"project": p.ID, "states": []string{"done"}}, "done"},
		{"unknown QA check", "list_units", map[string]any{"project": p.ID, "qa": []string{"nope"}}, "nope"},
		{"unknown sort", "list_units", map[string]any{"project": p.ID, "sort": "nope"}, "nope"},
		{"bad cursor", "list_units", map[string]any{"project": p.ID, "cursor": "garbage"}, "cursor"},
		{"half a cursor", "list_units", map[string]any{"project": p.ID, "cursor": "1"}, "cursor"},
		{"approved is not allowed", "save_translation", map[string]any{"id": ids["2"], "target": "Так", "state": "approved"}, "state"},
		{"untranslated is not allowed", "save_translation", map[string]any{"id": ids["2"], "target": "Так", "state": "untranslated"}, "state"},
		{"term without a source", "create_term", map[string]any{"project": p.ID}, "source"},
	}
	for _, c := range cases {
		text, isErr := call(t, cs, c.tool, c.args, nil)
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s: isError=%v text=%q, want an error mentioning %q", c.name, isErr, text, c.want)
		}
	}
	// An unknown tool is a protocol error, not a tool error.
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"}); err == nil || !strings.Contains(err.Error(), "no_such_tool") {
		t.Errorf("unknown tool: %v", err)
	}
	var n mcpCount
	call(t, cs, "count_units", map[string]any{"project": p.ID, "states": []string{"translated"}}, &n)
	if n.Count != before {
		t.Errorf("rejected calls changed data: %d -> %d translated", before, n.Count)
	}
}

// Keyset paging and sort orders work through the tool, and odd limits fall
// back to the defaults instead of failing.
func TestMCPPaging(t *testing.T) {
	cs, _, p := newMCPXLIFF(t, tmFixture())
	ids := unitIDs(t, cs, p.ID)

	walk := func(sort string, limit int) []string {
		var keys []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatalf("sort %q limit %d: paging never ends", sort, limit)
			}
			args := map[string]any{"project": p.ID, "limit": limit}
			if sort != "" {
				args["sort"] = sort
			}
			if cursor != "" {
				args["cursor"] = cursor
			}
			var page mcpPage
			if text, isErr := call(t, cs, "list_units", args, &page); isErr {
				t.Fatalf("sort %q: %s", sort, text)
			}
			for _, u := range page.Units {
				keys = append(keys, u.Key)
			}
			if page.Next == "" {
				return keys
			}
			cursor = page.Next
		}
	}
	all := []string{"a", "b", "c", "d", "e"}
	// Saves in the same second tie on updated_at, so the cursor's second
	// component has to break the tie without skipping or repeating a unit.
	call(t, cs, "save_translation", map[string]any{"id": ids["c"], "target": "Так"}, nil)
	for _, sort := range []string{"", "file", "short", "long", "updated"} {
		keys := walk(sort, 2)
		sorted := slices.Clone(keys)
		slices.Sort(sorted)
		if !slices.Equal(sorted, all) {
			t.Errorf("sort %q paged by 2 gave %v: every unit must appear exactly once", sort, keys)
		}
	}
	if keys := walk("", 2); !slices.Equal(keys, all) {
		t.Errorf("file order = %v", keys)
	}
	if keys := walk("short", 2); keys[0] != "e" {
		t.Errorf("shortest first = %v", keys)
	}
	if keys := walk("long", 2); keys[len(keys)-1] != "e" {
		t.Errorf("longest first = %v", keys)
	}

	// Limits outside 1..200 mean the default page, not an error.
	var page mcpPage
	for _, limit := range []int{0, -3, 1000} {
		var page mcpPage
		if text, isErr := call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": limit}, &page); isErr || len(page.Units) != 5 {
			t.Errorf("limit %d: %v %d units %s", limit, isErr, len(page.Units), text)
		}
	}
	// A page that is exactly full carries a cursor (there is no look-ahead,
	// as in the HTTP API); following it gives an empty last page.
	call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": 5}, &page)
	if len(page.Units) != 5 || page.Next == "" {
		t.Fatalf("full page = %d units, next %q", len(page.Units), page.Next)
	}
	var last mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": 5, "cursor": page.Next}, &last)
	if len(last.Units) != 0 || last.Next != "" {
		t.Errorf("page after the last = %d units, next %q", len(last.Units), last.Next)
	}
}

// A 70k-character string with a code goes in and out whole, while list rows
// stay previews and say so.
func TestMCPLongStrings(t *testing.T) {
	words := strings.Repeat("слово ", 11500) // ~69k characters
	src := words + `<ph id="1">{KEY}</ph>` + words
	plainSrc := words + "{KEY}" + words
	cs, _, p := newMCPXLIFF(t, xliff(`<trans-unit id="long"><source>`+src+`</source></trans-unit>`))
	ids := unitIDs(t, cs, p.ID)

	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	row := page.Units[0]
	if !row.Truncated || utf8.RuneCountInString(row.Source) >= utf8.RuneCountInString(plainSrc)/10 {
		t.Errorf("list row: truncated=%v, %d chars", row.Truncated, utf8.RuneCountInString(row.Source))
	}

	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": ids["long"]}, &u)
	if u.Source != plainSrc || !slices.Equal(u.Codes, []string{"{KEY}"}) {
		t.Fatalf("get_unit source: %d chars (want %d), codes %v", utf8.RuneCountInString(u.Source), utf8.RuneCountInString(plainSrc), u.Codes)
	}

	// Without its code a long target is refused, and the refusal names it.
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": ids["long"], "target": words + words}, nil); !isErr || !strings.Contains(text, "{KEY}") {
		t.Errorf("long target without its code: %v %.200s", isErr, text)
	}

	target := strings.ReplaceAll(plainSrc, "слово", "слова")
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": ids["long"], "target": target}, &u); isErr {
		t.Fatalf("save: %.200s", text)
	}
	call(t, cs, "get_unit", map[string]any{"id": ids["long"]}, &u)
	if u.Target == nil || *u.Target != target || u.State != "translated" {
		t.Errorf("round trip lost text: state %s, %d chars (want %d)", u.State, utf8.RuneCountInString(*u.Target), utf8.RuneCountInString(target))
	}
}

// MCP and the editor's HTTP API are one store: writes through either show up
// in the other and invalidate the HTTP ETags.
func TestMCPSharesState(t *testing.T) {
	cs, ts, p := newMCP(t)
	ids := unitIDs(t, cs, p.ID)
	count := "/api/projects/" + itoa(p.ID) + "/count?state=translated"

	res, b := do(t, ts, "GET", count, nil)
	tag := res.Header.Get("ETag")
	if string(strings.TrimSpace(string(b))) != `{"count":0}` {
		t.Fatalf("count before = %s", b)
	}
	call(t, cs, "save_translation", map[string]any{"id": ids["2"], "target": "Так"}, nil)
	res, b = do(t, ts, "GET", count, nil, "If-None-Match", tag)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != `{"count":1}` {
		t.Errorf("after an MCP save: %d %s (the ETag must not survive a write)", res.StatusCode, b)
	}

	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids["1"]), map[string]any{"target": "Націсніце {KEY}", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("http save: %d %s", res.StatusCode, b)
	}
	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": ids["1"], "history": true}, &u)
	if u.State != "translated" || u.Target == nil || *u.Target != "Націсніце {KEY}" || u.History[0].Origin != "edit" {
		t.Errorf("unit saved over HTTP, read over MCP = %+v", u)
	}
	var n mcpCount
	call(t, cs, "count_units", map[string]any{"project": p.ID, "states": []string{"translated"}}, &n)
	if n.Count != 2 {
		t.Errorf("translated count = %d, want 2", n.Count)
	}
}

// Saves from both sides at once leave the per-file counters, which triggers
// maintain, equal to what a query finds.
func TestMCPConcurrentWithHTTP(t *testing.T) {
	var units []string
	for i := range 12 {
		units = append(units, fmt.Sprintf(`<trans-unit id="u%d"><source>Строка номер %d</source></trans-unit>`, i, i))
	}
	cs, ts, p := newMCPXLIFF(t, xliff(units...))
	ids := unitIDs(t, cs, p.ID)

	var wg sync.WaitGroup
	for i := range 12 {
		id := ids[fmt.Sprintf("u%d", i)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "save_translation",
					Arguments: map[string]any{"id": id, "target": fmt.Sprintf("Радок %d", i)}})
				if err != nil || res.IsError {
					t.Errorf("mcp save %d: %v %+v", i, err, res)
				}
				return
			}
			body, _ := json.Marshal(map[string]any{"target": fmt.Sprintf("Радок %d", i), "state": "approved"})
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/units/%d", ts.URL, id), strings.NewReader(string(body)))
			res, err := http.DefaultClient.Do(req)
			if err != nil || res.StatusCode != http.StatusOK {
				t.Errorf("http save %d: %v %v", i, err, res)
			}
			if res != nil {
				res.Body.Close()
			}
		}()
	}
	wg.Wait()

	var projects []mcpProject
	call(t, cs, "list_projects", nil, &projects)
	if c := projects[0].Counts; c.Translated != 6 || c.Approved != 6 || c.Total != 12 {
		t.Errorf("counters = %+v, want 6 translated and 6 approved", c)
	}
	// The same numbers by query, through the search index.
	for _, state := range []string{"translated", "approved"} {
		var n mcpCount
		call(t, cs, "count_units", map[string]any{"project": p.ID, "states": []string{state}, "in": "target", "query": "Радок"}, &n)
		if n.Count != 6 {
			t.Errorf("%s count by search = %d, want 6", state, n.Count)
		}
	}
}
