package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
		"get_unit_tm", "get_unit_langs", "machine_translate", "save_translation", "propagate_translation",
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
