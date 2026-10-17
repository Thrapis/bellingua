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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

func newMCP(t *testing.T) (*mcp.ClientSession, *httptest.Server, *store.Project) {
	t.Helper()
	ts, p := newServer(t)
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, ts, p
}

// call runs a tool and decodes its JSON result into out (when non-nil). It
// returns the result text and whether the tool reported an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	// MCP clients reject structured content that is not a JSON object.
	if _, ok := res.StructuredContent.(map[string]any); res.StructuredContent != nil && !ok {
		t.Errorf("%s: structuredContent is %T, want a JSON object", name, res.StructuredContent)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if out != nil && !res.IsError {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatalf("%s: %v in %s", name, err, text)
		}
	}
	return text, res.IsError
}

// The tools are mounted at /mcp and listed.
func TestMCPToolList(t *testing.T) {
	cs, _, _ := newMCP(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"list_projects", "get_tree", "list_units", "count_units", "get_unit",
		"get_unit_tm", "get_unit_langs", "machine_translate", "save_translation", "save_translations", "replace_in_targets", "propagate_translation",
		"list_terms", "term_forms", "create_term", "update_term", "delete_term", "recheck_qa", "list_jobs", "get_job"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing tool %s (have %v)", want, names)
		}
	}
	if cs.InitializeResult().Instructions == "" {
		t.Error("no server instructions")
	}
}

// Units come out as plain strings with their codes, and paging works.
func TestMCPListAndGet(t *testing.T) {
	cs, _, p := newMCP(t)

	var res mcpProjects
	call(t, cs, "list_projects", nil, &res)
	projects := res.Projects
	if len(projects) != 1 || projects[0].ID != p.ID || projects[0].Counts.Total != 2 {
		t.Fatalf("projects = %+v", projects)
	}

	var tree mcpTree
	call(t, cs, "get_tree", map[string]any{"project": p.ID, "dir": "a/b"}, &tree)
	if len(tree.Children) != 1 || tree.Children[0].FileID == 0 {
		t.Errorf("tree = %+v", tree)
	}

	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID, "states": []string{"untranslated"}}, &page)
	if len(page.Units) != 1 || page.Units[0].Key != "2" || page.Units[0].Target != nil || page.Next != "" {
		t.Fatalf("untranslated = %+v", page)
	}

	call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": 1}, &page)
	if len(page.Units) != 1 || page.Next == "" {
		t.Fatalf("first page = %+v", page)
	}
	first := page.Units[0]
	if first.Source != "Нажмите {KEY}" || !slices.Equal(first.Codes, []string{"{KEY}"}) || first.State != "mt" {
		t.Errorf("row = %+v", first)
	}
	call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": 1, "cursor": page.Next}, &page)
	if len(page.Units) != 1 || page.Units[0].Key != "2" {
		t.Errorf("second page = %+v", page)
	}

	var n mcpCount
	call(t, cs, "count_units", map[string]any{"project": p.ID, "states": []string{"mt"}}, &n)
	if n.Count != 1 {
		t.Errorf("count = %d", n.Count)
	}

	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": first.ID}, &u)
	if u.Target == nil || *u.Target != "Націсніце {KEY}" || u.Path != "a/b/one.xlf" || u.Issues == nil {
		t.Errorf("unit = %+v", u)
	}

	if text, isErr := call(t, cs, "list_units", map[string]any{"project": p.ID, "qa": []string{"nope"}}, nil); !isErr || !strings.Contains(text, "nope") {
		t.Errorf("bad qa filter: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "get_unit", map[string]any{"id": 9999}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("missing unit: %v %s", isErr, text)
	}
}

// Saves are marked "ai", refuse broken codes and never touch human work.
func TestMCPSaveGuard(t *testing.T) {
	cs, ts, p := newMCP(t)
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	withCode, plain := page.Units[0], page.Units[1]

	text, isErr := call(t, cs, "save_translation", map[string]any{"id": withCode.ID, "target": "Націсніце"}, nil)
	if !isErr || !strings.Contains(text, "{KEY}") {
		t.Errorf("broken codes: %v %s", isErr, text)
	}

	var u mcpUnit
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": plain.ID, "target": "Так"}, &u); isErr {
		t.Fatalf("save: %s", text)
	}
	if u.State != "translated" || u.Target == nil || *u.Target != "Так" {
		t.Errorf("saved = %+v", u)
	}
	call(t, cs, "get_unit", map[string]any{"id": plain.ID, "history": true}, &u)
	if len(u.History) == 0 || u.History[0].Origin != "ai" { // newest first
		t.Errorf("history = %+v", u.History)
	}

	// The agent may revise its own translation.
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": plain.ID, "target": "Так!"}, &u); isErr || *u.Target != "Так!" {
		t.Errorf("revising own save: %v %s", isErr, text)
	}

	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": plain.ID, "target": "Так", "state": "approved"}, nil); !isErr || !strings.Contains(text, "state") {
		t.Errorf("approved state accepted: %v %s", isErr, text)
	}

	// A human approves it; the agent can no longer change it.
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(plain.ID), map[string]any{"target": "Так.", "state": "approved"}); res.StatusCode != http.StatusOK {
		t.Fatalf("human save: %d %s", res.StatusCode, b)
	}
	text, isErr = call(t, cs, "save_translation", map[string]any{"id": plain.ID, "target": "Не"}, nil)
	if !isErr || !strings.Contains(text, "not changed") {
		t.Errorf("guard: %v %s", isErr, text)
	}
	call(t, cs, "get_unit", map[string]any{"id": plain.ID}, &u)
	if *u.Target != "Так." || u.State != "approved" {
		t.Errorf("human work changed: %+v", u)
	}

	// MT state is allowed below translated.
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": withCode.ID, "target": "Націсніце {KEY}", "state": "mt"}, &u); isErr || u.State != "mt" {
		t.Errorf("mt save: %v %s", isErr, text)
	}
}

// The agent may overwrite anything below approved, including a human's
// translated text (the old text stays in the unit's history), but never an
// approved one.
func TestMCPOverwriteTranslated(t *testing.T) {
	cs, ts, p := newMCP(t)
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	id := page.Units[1].ID

	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(id), map[string]any{"target": "Так", "state": "translated"}); res.StatusCode != http.StatusOK {
		t.Fatalf("human save: %d %s", res.StatusCode, b)
	}
	var u mcpUnit
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": id, "target": "Не"}, &u); isErr || *u.Target != "Не" || u.State != "translated" {
		t.Fatalf("human translated text not overwritten: %v %s", isErr, text)
	}
	call(t, cs, "get_unit", map[string]any{"id": id, "history": true}, &u)
	var kept bool
	for _, h := range u.History {
		kept = kept || (h.Target != nil && *h.Target == "Так")
	}
	if len(u.History) < 2 || u.History[0].Origin != "ai" || !kept {
		t.Errorf("history = %+v", u.History)
	}

	// Once approved it is protected again.
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(id), map[string]any{"target": "Не", "state": "approved"}); res.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d %s", res.StatusCode, b)
	}
	if text, isErr := call(t, cs, "save_translation", map[string]any{"id": id, "target": "Так"}, nil); !isErr || !strings.Contains(text, "not changed") {
		t.Errorf("approved text overwritten: %v %s", isErr, text)
	}
}

func TestMCPTerms(t *testing.T) {
	cs, _, p := newMCP(t)
	var term mcpTermOut
	if text, isErr := call(t, cs, "create_term", map[string]any{"project": p.ID, "source": "нажмите", "target": "націсніце"}, &term); isErr {
		t.Fatalf("create: %s", text)
	}
	var terms mcpTerms
	call(t, cs, "list_terms", map[string]any{"project": p.ID, "contains": "нажм"}, &terms)
	if len(terms.Terms) != 1 || terms.Terms[0].ID != term.ID {
		t.Errorf("terms = %+v", terms)
	}
	call(t, cs, "list_terms", map[string]any{"project": p.ID, "contains": "zzz"}, &terms)
	if len(terms.Terms) != 0 {
		t.Errorf("filtered terms = %+v", terms)
	}

	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": page.Units[0].ID}, &u)
	if len(u.Terms) != 1 || !slices.Equal(u.Terms[0].Inserts, []string{"Націсніце"}) {
		t.Errorf("unit terms = %+v", u.Terms)
	}

	if text, isErr := call(t, cs, "update_term", map[string]any{"id": term.ID, "source": "нажмите", "target": "націсніце|клікніце", "note": "UI"}, &term); isErr || term.Note != "UI" {
		t.Errorf("update: %s", text)
	}
	if text, isErr := call(t, cs, "create_term", map[string]any{"project": p.ID, "source": "нажмите", "target": "x"}, nil); !isErr {
		t.Errorf("duplicate accepted: %s", text)
	}
	if text, isErr := call(t, cs, "delete_term", map[string]any{"id": term.ID}, nil); isErr {
		t.Errorf("delete: %s", text)
	}
	call(t, cs, "list_terms", map[string]any{"project": p.ID}, &terms)
	if len(terms.Terms) != 0 {
		t.Errorf("after delete = %+v", terms)
	}
}

func TestMCPJobsAndMT(t *testing.T) {
	cs, _, p := newMCP(t)
	var job Job
	if text, isErr := call(t, cs, "recheck_qa", map[string]any{"project": p.ID}, &job); isErr || job.ID == "" {
		t.Fatalf("recheck: %s", text)
	}
	var got Job
	if text, isErr := call(t, cs, "get_job", map[string]any{"id": job.ID}, &got); isErr || got.ID != job.ID {
		t.Errorf("get_job: %s", text)
	}

	// No MT provider in tests: a readable tool error, not a protocol failure.
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	if text, isErr := call(t, cs, "machine_translate", map[string]any{"id": page.Units[0].ID}, nil); !isErr || !strings.Contains(text, "provider") {
		t.Errorf("mt: %v %s", isErr, text)
	}
}

// Requests from other sites or through a rebound DNS name are refused.
func TestMCPRequestGuards(t *testing.T) {
	ts, _ := newServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	for name, hdr := range map[string][2]string{
		"rebound host": {"Host", "evil.example"},
		"cross origin": {"Origin", "https://evil.example"},
	} {
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if hdr[0] == "Host" {
			req.Host = hdr[1]
		} else {
			req.Header.Set(hdr[0], hdr[1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d", name, res.StatusCode)
		}
	}
}

// importRefs attaches an extra language to the test project (units "1" and
// "2" of a/b/one.xlf) and waits for the import job to list it.
func importRefs(t *testing.T, ts *httptest.Server, p *store.Project, lang string, units map[string]string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	srcs := map[string]string{"1": `Нажмите <ph id="1">{KEY}</ph>`, "2": "Да"}
	var body strings.Builder
	for _, id := range []string{"1", "2"} {
		tgt, ok := units[id]
		if !ok {
			continue
		}
		tgt = strings.Replace(tgt, "{KEY}", `<ph id="1">{KEY}</ph>`, 1)
		body.WriteString(`<trans-unit id="` + id + `"><source>` + srcs[id] + `</source><target>` + tgt + `</target></trans-unit>`)
	}
	xlf := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="` + lang + `"><body>` + body.String() + `</body></file></xliff>`
	os.WriteFile(filepath.Join(dir, "a", "b", "one.xlf"), []byte(xlf), 0o644)
	pid := "/api/projects/" + itoa(p.ID)
	if res, b := do(t, ts, "POST", pid+"/langs/import", map[string]any{"dir": dir}); res.StatusCode != http.StatusAccepted {
		t.Fatalf("import %s: %d %s", lang, res.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := do(t, ts, "GET", pid+"/langs", nil)
		if strings.Contains(string(b), `"lang":"`+lang+`"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("language %s never listed: %s", lang, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// list_units can carry the extra languages' texts: present ones as cut
// previews, missing ones left out, and only the requested languages.
func TestMCPListRefs(t *testing.T) {
	cs, ts, p := newMCP(t)
	long := strings.Repeat("я", store.PreviewRunes+100)
	importRefs(t, ts, p, "uk", map[string]string{"1": "Натисніть {KEY}", "2": long})
	importRefs(t, ts, p, "en", map[string]string{"1": "Press {KEY}"})

	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	if len(page.Units) != 2 || page.Units[0].Refs != nil || page.Units[1].Refs != nil || page.Units[0].RefsTruncated {
		t.Fatalf("refs without langs: %+v", page.Units)
	}

	call(t, cs, "list_units", map[string]any{"project": p.ID, "langs": []string{"uk", "en"}}, &page)
	one, two := page.Units[0], page.Units[1]
	if one.Refs["uk"] != "Натисніть {KEY}" || one.Refs["en"] != "Press {KEY}" || one.RefsTruncated {
		t.Errorf("unit 1 refs = %+v", one)
	}
	if len(two.Refs) != 1 || !two.RefsTruncated || len([]rune(two.Refs["uk"])) != store.PreviewRunes+1 || !strings.HasSuffix(two.Refs["uk"], "…") {
		t.Errorf("unit 2 refs = %d runes, %+v", len([]rune(two.Refs["uk"])), two)
	}
	if _, has := two.Refs["en"]; has {
		t.Errorf("missing en text invented: %+v", two.Refs)
	}

	// Only the asked languages come back; unknown ones are simply absent.
	page = mcpPage{} // Unmarshal would merge into the earlier maps
	call(t, cs, "list_units", map[string]any{"project": p.ID, "langs": []string{"en", "xx"}}, &page)
	if len(page.Units[0].Refs) != 1 || page.Units[0].Refs["en"] != "Press {KEY}" || page.Units[1].Refs != nil {
		t.Errorf("filtered refs = %+v", page.Units)
	}
}

func saveBatch(t *testing.T, cs *mcp.ClientSession, items ...map[string]any) (mcpBatch, string, bool) {
	t.Helper()
	var out mcpBatch
	text, isErr := call(t, cs, "save_translations", map[string]any{"items": items}, &out)
	return out, text, isErr
}

// One batch with a good item, a code mismatch, an approved unit and a missing
// unit: each gets its own result and the bad ones don't stop the rest.
func TestMCPSaveTranslations(t *testing.T) {
	cs, ts, p := newMCP(t)
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	withCode, plain := page.Units[0], page.Units[1]
	if res, b := do(t, ts, "PUT", "/api/units/"+itoa(plain.ID), map[string]any{"target": "Так.", "state": "approved"}); res.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d %s", res.StatusCode, b)
	}

	out, text, isErr := saveBatch(t, cs,
		map[string]any{"id": withCode.ID, "target": "Націсніце"}, // codes missing
		map[string]any{"id": plain.ID, "target": "Не"},           // approved
		map[string]any{"id": 9999, "target": "Не"},               // no such unit
		map[string]any{"id": withCode.ID, "target": "Націсніце {KEY}!"},
	)
	if isErr || len(out.Results) != 4 {
		t.Fatalf("batch: %v %s", isErr, text)
	}
	r := out.Results
	if r[0].OK || r[0].ID != withCode.ID || !strings.Contains(r[0].Error, "{KEY}") {
		t.Errorf("mismatch = %+v", r[0])
	}
	if r[1].OK || !strings.Contains(r[1].Error, "not changed") {
		t.Errorf("approved = %+v", r[1])
	}
	if r[2].OK || !strings.Contains(r[2].Error, "not found") {
		t.Errorf("missing = %+v", r[2])
	}
	if !r[3].OK || r[3].Error != "" || r[3].ID != withCode.ID {
		t.Errorf("good = %+v", r[3])
	}
	if out.Saved != 1 || out.Failed != 3 {
		t.Errorf("counts = %d saved, %d failed", out.Saved, out.Failed)
	}

	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": withCode.ID, "history": true}, &u)
	if *u.Target != "Націсніце {KEY}!" || u.State != "translated" || len(u.History) == 0 || u.History[0].Origin != "ai" {
		t.Errorf("saved unit = %+v", u)
	}
	call(t, cs, "get_unit", map[string]any{"id": plain.ID}, &u)
	if *u.Target != "Так." || u.State != "approved" {
		t.Errorf("approved unit changed: %+v", u)
	}

	// A state other than translated/mt is refused for that item.
	out, _, _ = saveBatch(t, cs, map[string]any{"id": withCode.ID, "target": "Так", "state": "approved"})
	if len(out.Results) != 1 || out.Results[0].OK || !strings.Contains(out.Results[0].Error, "state") {
		t.Errorf("approved state accepted: %+v", out.Results)
	}
}

// Without a target the unit keeps its text and becomes translated ("mark
// reviewed"); a unit with no text has nothing to keep.
func TestMCPSaveTranslationsKeepsText(t *testing.T) {
	cs, _, p := newMCP(t)
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	withCode, plain := page.Units[0], page.Units[1]
	if withCode.State != "mt" || plain.Target != nil {
		t.Fatalf("fixture changed: %+v", page.Units)
	}

	out, text, isErr := saveBatch(t, cs, map[string]any{"id": withCode.ID}, map[string]any{"id": plain.ID})
	if isErr || len(out.Results) != 2 {
		t.Fatalf("batch: %v %s", isErr, text)
	}
	if !out.Results[0].OK {
		t.Errorf("mark reviewed = %+v", out.Results[0])
	}
	if out.Results[1].OK || !strings.Contains(out.Results[1].Error, "no translation") {
		t.Errorf("untranslated without target = %+v", out.Results[1])
	}

	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": withCode.ID, "history": true}, &u)
	if *u.Target != "Націсніце {KEY}" || u.State != "translated" || u.History[0].Origin != "ai" {
		t.Errorf("kept unit = %+v", u)
	}
	call(t, cs, "get_unit", map[string]any{"id": plain.ID}, &u)
	if u.Target != nil || u.State != "untranslated" {
		t.Errorf("untranslated unit changed: %+v", u)
	}

	// Keeping the text but marking it mt is allowed too.
	out, _, _ = saveBatch(t, cs, map[string]any{"id": withCode.ID, "state": "mt"})
	call(t, cs, "get_unit", map[string]any{"id": withCode.ID}, &u)
	if !out.Results[0].OK || u.State != "mt" || *u.Target != "Націсніце {KEY}" {
		t.Errorf("keep as mt: %+v %+v", out.Results, u)
	}
}

func TestMCPSaveTranslationsLimit(t *testing.T) {
	cs, _, p := newMCP(t)
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID}, &page)
	id := page.Units[1].ID

	items := make([]map[string]any, 0, 201)
	for i := 0; i < 200; i++ {
		items = append(items, map[string]any{"id": id, "target": "Так"})
	}
	out, text, isErr := saveBatch(t, cs, items...)
	if isErr || len(out.Results) != 200 || out.Saved != 200 {
		t.Fatalf("200 items: %v %.200s", isErr, text)
	}
	items = append(items, map[string]any{"id": id, "target": "Так"})
	if text, isErr := call(t, cs, "save_translations", map[string]any{"items": items}, nil); !isErr || !strings.Contains(text, "200") {
		t.Errorf("201 items: %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "save_translations", map[string]any{"items": []map[string]any{}}, nil); !isErr {
		t.Errorf("no items accepted: %s", text)
	}
}

// --- replace_in_targets ------------------------------------------------------

// sweepUnit is one fixture unit: raw XLIFF source and target (no target = untranslated).
type sweepUnit struct{ key, src, tgt string }

// newSweep serves a project holding exactly these units (imported as mt) and
// returns an MCP client, the HTTP server and the unit ids by key.
func newSweep(t *testing.T, units []sweepUnit) (*mcp.ClientSession, *httptest.Server, *store.Project, map[string]int64) {
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
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>`)
	for _, u := range units {
		b.WriteString(`<trans-unit id="` + u.key + `"><source>` + u.src + `</source>`)
		if u.tgt != "" {
			b.WriteString(`<target state="needs-review-translation">` + u.tgt + `</target>`)
		}
		b.WriteString(`</trans-unit>`)
	}
	b.WriteString(`</body></file></xliff>`)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "one.xlf"), []byte(b.String()), 0o644)
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(New(ctx, st, config.Default(), nil, bm, nil, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	var page mcpPage
	call(t, cs, "list_units", map[string]any{"project": p.ID, "limit": 200}, &page)
	ids := map[string]int64{}
	for _, r := range page.Units {
		ids[r.Key] = r.ID
	}
	if len(ids) != len(units) {
		t.Fatalf("fixture has %d units, want %d", len(ids), len(units))
	}
	return cs, ts, p, ids
}

const keyPH = `<ph id="1">{KEY}</ph>`

// sweepFixture: k1 has a code, k2 an inflected form plus the exact one, k3/k4
// differ in case and word boundaries, k5 is approved, k6 does not match, k7 is
// human-translated, k8 would break its code, k9 is untranslated.
func sweepFixture(t *testing.T) (*mcp.ClientSession, *httptest.Server, *store.Project, map[string]int64) {
	cs, ts, p, ids := newSweep(t, []sweepUnit{
		{"k1", "Нажмите " + keyPH, "Сеткавы дазор " + keyPH + " тут"},
		{"k2", "Один", "Сеткавага дазору няма. Сеткавы дазор."},
		{"k3", "Два", "Рыпер ідзе. рыпер стаіць."},
		{"k4", "Три", "рыперам і рыпер"},
		{"k5", "Четыре", "Сеткавы дазор"},
		{"k6", "Пять", "Нічога"},
		{"k7", "Шесть", "Сеткавы дазор ужо"},
		{"k8", "Нажмите " + keyPH, "Адкрыйце " + keyPH + " ключ"},
		{"k9", "Девять", ""},
	})
	for key, state := range map[string]string{"k5": "approved", "k7": "translated"} {
		target := map[string]string{"k5": "Сеткавы дазор", "k7": "Сеткавы дазор ужо"}[key]
		if res, b := do(t, ts, "PUT", "/api/units/"+itoa(ids[key]), map[string]any{"target": target, "state": state}); res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", key, res.StatusCode, b)
		}
	}
	return cs, ts, p, ids
}

func sweep(t *testing.T, cs *mcp.ClientSession, p *store.Project, args map[string]any) (mcpSweep, string, bool) {
	t.Helper()
	args["project"] = p.ID
	var out mcpSweep
	text, isErr := call(t, cs, "replace_in_targets", args, &out)
	return out, text, isErr
}

func unitOf(t *testing.T, cs *mcp.ClientSession, id int64) mcpUnit {
	t.Helper()
	var u mcpUnit
	call(t, cs, "get_unit", map[string]any{"id": id, "history": true}, &u)
	return u
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func pair(find, repl string) map[string]any { return map[string]any{"find": find, "replace": repl} }

func sampleOf(out mcpSweep, id int64) (mcpSweepSample, bool) {
	for _, s := range out.Samples {
		if s.ID == id {
			return s, true
		}
	}
	return mcpSweepSample{}, false
}

// Without dryRun=false nothing is written, yet the report says what would change.
func TestMCPReplaceDryRunByDefault(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	before := map[string]mcpUnit{}
	for k, id := range ids {
		before[k] = unitOf(t, cs, id)
	}
	out, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}})
	if isErr {
		t.Fatal(text)
	}
	// k1, k2 (mt) and k7 (translated); k5 is approved and left out by the default states.
	if !out.DryRun || out.Matched != 3 || out.Changed != 3 || out.Skipped != 0 || out.Failed != 0 || out.Next != "" {
		t.Errorf("dry run = %+v", out)
	}
	s, ok := sampleOf(out, ids["k2"])
	if !ok || s.Before != "Сеткавага дазору няма. Сеткавы дазор." || s.After != "Сеткавага дазору няма. Сеткавая варта." {
		t.Errorf("sample = %+v", s)
	}
	for k, id := range ids {
		u := unitOf(t, cs, id)
		if derefStr(u.Target) != derefStr(before[k].Target) || u.State != before[k].State || len(u.History) != len(before[k].History) {
			t.Errorf("%s changed by a dry run: %+v", k, u)
		}
	}
	again, _, _ := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": true})
	if !again.DryRun || again.Changed != 3 {
		t.Errorf("explicit dry run = %+v", again)
	}
}

// A real run changes only the units that match, keeps their states, writes
// history with origin ai and leaves approved units and codes alone.
func TestMCPReplaceRealRun(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	out, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": false,
		"states": []string{"mt", "translated", "approved"}})
	if isErr {
		t.Fatal(text)
	}
	if out.DryRun || out.Matched != 4 || out.Changed != 3 || out.Skipped != 1 || out.Failed != 0 {
		t.Errorf("run = %+v", out)
	}
	want := map[string]struct{ target, state string }{
		"k1": {"Сеткавая варта {KEY} тут", "mt"},
		"k2": {"Сеткавага дазору няма. Сеткавая варта.", "mt"},
		"k3": {"Рыпер ідзе. рыпер стаіць.", "mt"},
		"k5": {"Сеткавы дазор", "approved"},
		"k6": {"Нічога", "mt"},
		"k7": {"Сеткавая варта ужо", "translated"},
	}
	for k, w := range want {
		u := unitOf(t, cs, ids[k])
		if u.Target == nil || *u.Target != w.target || u.State != w.state {
			t.Errorf("%s = %v %s, want %q %s", k, u.Target, u.State, w.target, w.state)
		}
	}
	for _, k := range []string{"k1", "k2", "k7"} {
		if h := unitOf(t, cs, ids[k]).History; len(h) == 0 || h[0].Origin != "ai" {
			t.Errorf("%s history = %+v", k, h)
		}
	}
	if h := unitOf(t, cs, ids["k6"]).History; len(h) != 0 {
		t.Errorf("untouched unit has history %+v", h)
	}
	if u := unitOf(t, cs, ids["k9"]); u.Target != nil || u.State != "untranslated" {
		t.Errorf("untranslated unit = %+v", u)
	}
	// The old form is gone, so a second run has nothing to do.
	again, _, _ := sweep(t, cs, p, map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "dryRun": false})
	if again.Matched != 0 || again.Changed != 0 {
		t.Errorf("second run = %+v", again)
	}
}

// Pairs apply in order: a later pair sees the earlier one's result.
func TestMCPReplacePairsInOrder(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	out, text, isErr := sweep(t, cs, p, map[string]any{"query": "рыпер",
		"pairs": []any{pair("Рыпер", "Хлопец"), pair("Хлопец", "Рыпердок")}})
	if isErr {
		t.Fatal(text)
	}
	if s, ok := sampleOf(out, ids["k3"]); !ok || s.After != "Рыпердок ідзе. рыпер стаіць." {
		t.Errorf("sample = %+v", s)
	}
	// Reversed order: the first pair finds nothing to chain on.
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "рыпер",
		"pairs": []any{pair("Хлопец", "Рыпердок"), pair("Рыпер", "Хлопец")}})
	if s, _ := sampleOf(out, ids["k3"]); s.After != "Хлопец ідзе. рыпер стаіць." {
		t.Errorf("reversed = %+v", s)
	}
}

func TestMCPReplaceIgnoreCaseAndWholeWord(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	run := func(extra map[string]any) mcpSweep {
		args := map[string]any{"query": "рыпер", "pairs": []any{pair("рыпер", "рыпердок")}}
		for k, v := range extra {
			args[k] = v
		}
		out, text, isErr := sweep(t, cs, p, args)
		if isErr {
			t.Fatal(text)
		}
		return out
	}
	after := func(out mcpSweep, key string) string {
		s, _ := sampleOf(out, ids[key])
		return s.After
	}

	out := run(nil) // case-sensitive: the capitalised Рыпер stays
	if after(out, "k3") != "Рыпер ідзе. рыпердок стаіць." || after(out, "k4") != "рыпердокам і рыпердок" {
		t.Errorf("case-sensitive = %+v", out.Samples)
	}
	out = run(map[string]any{"ignoreCase": true})
	if after(out, "k3") != "рыпердок ідзе. рыпердок стаіць." || after(out, "k4") != "рыпердокам і рыпердок" {
		t.Errorf("ignoreCase = %+v", out.Samples)
	}
	out = run(map[string]any{"wholeWord": true}) // «рыперам» is another word
	if after(out, "k4") != "рыперам і рыпердок" || after(out, "k3") != "Рыпер ідзе. рыпердок стаіць." {
		t.Errorf("wholeWord = %+v", out.Samples)
	}
	out = run(map[string]any{"wholeWord": true, "ignoreCase": true})
	if after(out, "k3") != "рыпердок ідзе. рыпердок стаіць." || after(out, "k4") != "рыперам і рыпердок" {
		t.Errorf("both = %+v", out.Samples)
	}
	if out.Changed != 2 {
		t.Errorf("both changed = %d", out.Changed)
	}
}

// Letters of any script and digits join words, so such neighbours block a
// whole-word match; an apostrophe between letters stays inside the word.
func TestMCPReplaceWholeWordBoundaries(t *testing.T) {
	cs, _, p, ids := newSweep(t, []sweepUnit{
		{"a", "A", "сакс, (сакс) сакс-два сакс2 2сакс эсакс саксы сакс"},
		{"b", "B", "аб'ект"},
	})
	out, text, isErr := sweep(t, cs, p, map[string]any{"query": "сакс", "wholeWord": true, "pairs": []any{pair("сакс", "X")}})
	if isErr {
		t.Fatal(text)
	}
	if s, _ := sampleOf(out, ids["a"]); s.After != "X, (X) X-два сакс2 2сакс эсакс саксы X" {
		t.Errorf("boundaries = %q", s.After)
	}
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "аб", "wholeWord": true, "pairs": []any{pair("аб", "X")}})
	if out.Matched != 0 {
		t.Errorf("apostrophe split a word: %+v", out)
	}
}

// A replacement that breaks the code match is refused for that unit only, in a
// dry run as well as a real one.
func TestMCPReplaceCodeGuard(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	args := map[string]any{"query": "ключ", "pairs": []any{pair("ключ", "{KEY}")}}
	out, text, isErr := sweep(t, cs, p, args)
	if isErr {
		t.Fatal(text)
	}
	if out.Matched != 1 || out.Changed != 0 || out.Failed != 1 || len(out.Failures) != 1 ||
		out.Failures[0].ID != ids["k8"] || !strings.Contains(out.Failures[0].Error, "{KEY}") {
		t.Errorf("dry run = %+v", out)
	}
	args["dryRun"] = false
	out, _, _ = sweep(t, cs, p, args)
	if out.Changed != 0 || out.Failed != 1 {
		t.Errorf("real run = %+v", out)
	}
	if u := unitOf(t, cs, ids["k8"]); *u.Target != "Адкрыйце {KEY} ключ" || len(u.History) != 0 {
		t.Errorf("refused unit changed: %+v", u)
	}

	// A replacement that keeps every code is fine, even next to one.
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "{KEY}", "dryRun": false, "pairs": []any{pair("{KEY}", "{KEY}!")}})
	if out.Failed != 0 || out.Changed != 2 {
		// "{KEY}!" keeps one "{KEY}", so this is a valid edit that merely adds text after the code.
		t.Errorf("code-preserving edit = %+v", out)
	}
	if u := unitOf(t, cs, ids["k1"]); *u.Target != "Сеткавы дазор {KEY}! тут" {
		t.Errorf("code unit = %q", *u.Target)
	}

	// One refused unit among good ones: the rest still change.
	out, _, _ = sweep(t, cs, p, map[string]any{"query": "!", "dryRun": false, "pairs": []any{pair("{KEY}!", "")}})
	if out.Failed != 2 || out.Changed != 0 {
		t.Errorf("dropped codes = %+v", out)
	}
	var tree mcpTree
	call(t, cs, "get_tree", map[string]any{"project": p.ID}, &tree)
	mixed, _, _ := sweep(t, cs, p, map[string]any{"file": tree.Children[0].FileID, "dryRun": false,
		"pairs": []any{pair("ключ", "{KEY}"), pair("дазор", "варта")}})
	if mixed.Failed != 1 || mixed.Failures[0].ID != ids["k8"] || mixed.Changed != 3 {
		t.Errorf("mixed = %+v", mixed)
	}
}

// Samples are capped and cut around the first change.
func TestMCPReplaceSamples(t *testing.T) {
	long := strings.Repeat("Слова слова. ", 100)
	units := []sweepUnit{{"long", "L", long + "ЦЕЛЬ" + long}}
	for i := 0; i < 12; i++ {
		units = append(units, sweepUnit{fmt.Sprintf("s%d", i), "S", "ЦЕЛЬ"})
	}
	cs, _, p, ids := newSweep(t, units)
	out, text, isErr := sweep(t, cs, p, map[string]any{"pairs": []any{pair("ЦЕЛЬ", "МЭТА")}})
	if isErr {
		t.Fatal(text)
	}
	if out.Changed != 13 || len(out.Samples) != 10 {
		t.Fatalf("changed %d, %d samples", out.Changed, len(out.Samples))
	}
	s, ok := sampleOf(out, ids["long"])
	if !ok {
		t.Fatal("the first unit is not sampled")
	}
	if n := len([]rune(s.Before)); n > 320 || !strings.Contains(s.Before, "ЦЕЛЬ") || !strings.Contains(s.After, "МЭТА") || strings.Contains(s.After, "ЦЕЛЬ") {
		t.Errorf("long sample (%d runes) = %+v", n, s)
	}
}

// limit caps the matched units per call and next resumes after them.
func TestMCPReplaceLimitAndCursor(t *testing.T) {
	var units []sweepUnit
	for i := 0; i < 5; i++ {
		units = append(units, sweepUnit{fmt.Sprintf("u%d", i), "U", fmt.Sprintf("Сеткавы дазор %d", i)})
	}
	cs, _, p, ids := newSweep(t, units)
	args := func(cursor string) map[string]any {
		a := map[string]any{"pairs": []any{pair("Сеткавы дазор", "Сеткавая варта")}, "limit": 2}
		if cursor != "" {
			a["cursor"] = cursor
		}
		return a
	}
	seen := map[int64]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		out, text, isErr := sweep(t, cs, p, args(cursor))
		if isErr {
			t.Fatal(text)
		}
		want := 2
		if page == 2 {
			want = 1
		}
		if out.Matched != want || out.Changed != want {
			t.Fatalf("page %d = %+v", page, out)
		}
		if page < 2 && out.Next == "" {
			t.Fatalf("page %d has no next", page)
		}
		for _, s := range out.Samples {
			if seen[s.ID] {
				t.Errorf("unit %d on two pages", s.ID)
			}
			seen[s.ID] = true
		}
		cursor = out.Next
	}
	if cursor != "" || len(seen) != 5 {
		t.Errorf("after 3 pages: next %q, %d units", cursor, len(seen))
	}

	// A real run with a limit changes that many; running again carries on with the rest.
	a := args("")
	a["dryRun"] = false
	out, _, _ := sweep(t, cs, p, a)
	if out.Changed != 2 || out.Next == "" {
		t.Fatalf("real run = %+v", out)
	}
	changed := 0
	for _, id := range ids {
		if strings.HasPrefix(*unitOf(t, cs, id).Target, "Сеткавая варта") {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("%d units changed, want 2", changed)
	}
	if out, _, _ = sweep(t, cs, p, a); out.Changed != 2 {
		t.Errorf("second run = %+v", out)
	}
	delete(a, "limit") // the default limit takes the rest
	if out, _, _ = sweep(t, cs, p, a); out.Changed != 1 || out.Next != "" {
		t.Errorf("last run = %+v", out)
	}
}

// Bad input is an error before anything is touched.
func TestMCPReplaceErrors(t *testing.T) {
	cs, _, p, ids := sweepFixture(t)
	many := make([]any, 51)
	for i := range many {
		many[i] = pair("a", "b")
	}
	cases := map[string]func(a map[string]any){
		"unknown state": func(a map[string]any) { a["states"] = []string{"bogus"} },
		"unknown qa":    func(a map[string]any) { a["qa"] = []string{"bogus"} },
		"bad cursor":    func(a map[string]any) { a["cursor"] = "x" },
		"no pairs":      func(a map[string]any) { a["pairs"] = []any{} },
		"51 pairs":      func(a map[string]any) { a["pairs"] = many },
		"empty find":    func(a map[string]any) { a["pairs"] = []any{pair("", "X")} },
		"no query":      func(a map[string]any) { a["pairs"] = []any{pair("Рыпер", "X"), pair("Сеткавы", "Y")} },
		"no project":    func(a map[string]any) { a["project"] = 9999 },
	}
	for name, mod := range cases {
		a := map[string]any{"project": p.ID, "pairs": []any{pair("Сеткавы дазор", "X")}, "dryRun": false}
		mod(a)
		if text, isErr := call(t, cs, "replace_in_targets", a, nil); !isErr {
			t.Errorf("%s accepted: %s", name, text)
		}
	}
	for k, id := range ids {
		if h := unitOf(t, cs, id).History; len(h) > 0 && h[0].Origin == "ai" {
			t.Errorf("%s written by a refused call", k)
		}
	}
}

// Narrowing by file lets several pairs run without a query.
func TestMCPReplaceFilter(t *testing.T) {
	cs, _, p, _ := sweepFixture(t)
	var tree mcpTree
	call(t, cs, "get_tree", map[string]any{"project": p.ID}, &tree)
	if len(tree.Children) == 0 {
		t.Fatal("no files")
	}
	out, text, isErr := sweep(t, cs, p, map[string]any{"file": tree.Children[0].FileID,
		"pairs": []any{pair("Рыпер", "X"), pair("Сеткавы", "Y")}})
	if isErr || out.Changed == 0 {
		t.Errorf("file-narrowed sweep = %+v %s", out, text)
	}
}
