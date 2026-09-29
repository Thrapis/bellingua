package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

func newServer(t *testing.T) (*httptest.Server, *store.Project) {
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
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	xlf := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>
<trans-unit id="1"><source>Нажмите <ph id="1">{KEY}</ph></source><target state="needs-review-translation">Націсніце <ph id="1">{KEY}</ph></target></trans-unit>
<trans-unit id="2"><source>Да</source></trans-unit>
</body></file></xliff>`
	os.WriteFile(filepath.Join(dir, "a", "b", "one.xlf"), []byte(xlf), 0o644)
	srvCfg := config.Default()
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	static := fstest.MapFS{
		"index.html":  {Data: []byte("home")},
		"editor.html": {Data: []byte("editor")},
	}
	s := New(ctx, st, srvCfg, nil, bm, static, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, p
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	return res, buf.Bytes()
}

func TestAPIFlow(t *testing.T) {
	ts, p := newServer(t)
	pid := "/api/projects/" + itoa(p.ID)

	// Tree: lazy levels with rolled-up counts.
	_, b := do(t, ts, "GET", pid+"/tree", nil)
	var tree struct {
		Children []TreeNode `json:"children"`
	}
	json.Unmarshal(b, &tree)
	if len(tree.Children) != 1 || tree.Children[0].Path != "a" || tree.Children[0].Counts.Total != 2 {
		t.Fatalf("root = %s", b)
	}
	_, b = do(t, ts, "GET", pid+"/tree?dir=a/b", nil)
	if !strings.Contains(string(b), `"file":`) {
		t.Errorf("a/b = %s", b)
	}

	// ETag: unchanged data answers 304.
	res, _ := do(t, ts, "GET", pid+"/count", nil)
	tag := res.Header.Get("ETag")
	if res, _ := do(t, ts, "GET", pid+"/count", nil, "If-None-Match", tag); res.StatusCode != http.StatusNotModified {
		t.Errorf("etag: status %d", res.StatusCode)
	}

	// List with a state filter.
	_, b = do(t, ts, "GET", pid+"/units?state=untranslated", nil)
	var page unitsPage
	json.Unmarshal(b, &page)
	if len(page.Rows) != 1 || page.Rows[0].Key != "2" || page.Next != nil {
		t.Fatalf("untranslated = %s", b)
	}
	_, b = do(t, ts, "GET", pid+"/units", nil)
	json.Unmarshal(b, &page)
	withCode := page.Rows[0]

	// Save: broken placeholders are refused with the details.
	u := "/api/units/" + itoa(withCode.ID)
	res, b = do(t, ts, "PUT", u, map[string]any{"target": "Націсніце", "state": "translated"})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "{KEY}") {
		t.Errorf("broken save: %d %s", res.StatusCode, b)
	}
	res, b = do(t, ts, "PUT", u, map[string]any{"target": "Націсніце {KEY}!", "state": "approved"})
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), `"state":"approved"`) || !strings.Contains(string(b), `"punctuation"`) {
		t.Errorf("save: %d %s", res.StatusCode, b)
	}
	if res, _ := do(t, ts, "GET", pid+"/count", nil, "If-None-Match", tag); res.StatusCode != http.StatusOK {
		t.Errorf("etag not invalidated by a write: %d", res.StatusCode)
	}
	_, b = do(t, ts, "GET", pid+"/count?state=approved", nil)
	if string(bytes.TrimSpace(b)) != `{"count":1}` {
		t.Errorf("approved count = %s", b)
	}

	// Bulk: nothing else has a translation without errors besides unit 1.
	_, b = do(t, ts, "POST", pid+"/bulk?state=mt", map[string]any{"state": "approved", "noErrors": true})
	if !strings.Contains(string(b), `"changed":0`) {
		t.Errorf("bulk = %s", b)
	}

	// Unknown filter values are client errors.
	if res, _ := do(t, ts, "GET", pid+"/units?qa=nope", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad qa filter: %d", res.StatusCode)
	}
}

func TestSPA(t *testing.T) {
	ts, _ := newServer(t)
	for path, want := range map[string]string{"/": "home", "/editor": "editor", "/editor?project=1": "editor", "/nope": "home"} {
		res, b := do(t, ts, "GET", path, nil)
		if res.StatusCode != 200 || string(b) != want {
			t.Errorf("%s: %d %q", path, res.StatusCode, b)
		}
	}
	if res, _ := do(t, ts, "GET", "/api/nope", nil); res.StatusCode != 404 {
		t.Errorf("unknown api: %d", res.StatusCode)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestGlossaryAPI(t *testing.T) {
	ts, p := newServer(t)
	pid := "/api/projects/" + itoa(p.ID)

	res, b := do(t, ts, "POST", pid+"/terms", map[string]any{"source": "нажми", "target": "націсні"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", res.StatusCode, b)
	}
	if res, _ := do(t, ts, "POST", pid+"/terms", map[string]any{"source": "нажми"}); res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate: %d", res.StatusCode)
	}
	if res, _ := do(t, ts, "POST", pid+"/terms", map[string]any{"source": "  "}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("empty source: %d", res.StatusCode)
	}

	// Unit 1 is «Нажмите {KEY}» / «Націсніце {KEY}»: «Нажмите» stems like
	// «нажми», and «Націсніце» starts with the target's prefix «націс».
	_, b = do(t, ts, "GET", pid+"/units?state=mt", nil)
	var page unitsPage
	json.Unmarshal(b, &page)
	_, b = do(t, ts, "GET", "/api/units/"+itoa(page.Rows[0].ID), nil)
	var u struct {
		Terms []struct {
			Source string   `json:"source"`
			Spans  [][2]int `json:"spans"`
		} `json:"terms"`
		Issues []struct{ Check string } `json:"issues"`
	}
	json.Unmarshal(b, &u)
	if len(u.Terms) != 1 || u.Terms[0].Spans[0] != [2]int{0, 7} {
		t.Fatalf("terms = %s", b)
	}
	if len(u.Issues) != 0 {
		t.Errorf("agreed translation flagged: %s", b)
	}

	// Change the agreed target via CSV import: now the unit violates it.
	req, _ := http.NewRequest("POST", ts.URL+pid+"/terms/import", strings.NewReader("source,target\nнажми,адцісні\nновый,новы\n"))
	res2, err := http.DefaultClient.Do(req)
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("import: %v %v", res2.StatusCode, err)
	}
	var imp map[string]int
	json.NewDecoder(res2.Body).Decode(&imp)
	res2.Body.Close()
	if imp["inserted"] != 1 || imp["updated"] != 1 {
		t.Errorf("import = %v", imp)
	}
	_, b = do(t, ts, "GET", "/api/units/"+itoa(page.Rows[0].ID), nil)
	if !strings.Contains(string(b), `"check":"glossary"`) {
		t.Errorf("glossary issue missing after the change: %s", b)
	}

	_, b = do(t, ts, "GET", pid+"/terms/export", nil)
	if !strings.HasPrefix(string(b), "source,target,note,dnt,case_sensitive,forms\n") || !strings.Contains(string(b), "нажми,адцісні") {
		t.Errorf("export = %s", b)
	}

	// The debounced recheck updates the stored QA mask (and the counters).
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b = do(t, ts, "GET", pid+"/count?qa=glossary", nil)
		if strings.Contains(string(b), `"count":1`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recheck never flagged the unit: %s", b)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Word forms: found in the texts, stored via CSV, used for insertion.
	_, b = do(t, ts, "GET", pid+"/terms/forms?source="+url.QueryEscape("нажми"), nil)
	if !strings.Contains(string(b), `{"form":"Нажмите","count":1}`) {
		t.Errorf("forms = %s", b)
	}
	req, _ = http.NewRequest("POST", ts.URL+pid+"/terms/import", strings.NewReader("source,target,forms\nнажми,адцісні,нажмите=адцісніце; broken\n"))
	if res2, err = http.DefaultClient.Do(req); err != nil || res2.StatusCode != 200 {
		t.Fatalf("import forms: %v", err)
	}
	res2.Body.Close()
	_, b = do(t, ts, "GET", pid+"/terms/export", nil)
	if !strings.Contains(string(b), "нажми,адцісні,,,,нажмите=адцісніце\n") {
		t.Errorf("export with forms = %s", b)
	}
	_, b = do(t, ts, "GET", "/api/units/"+itoa(page.Rows[0].ID), nil)
	if !strings.Contains(string(b), `"inserts":["Адцісніце"]`) {
		t.Errorf("form insert missing: %s", b)
	}
}

func TestTranslationMemory(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, _ := st.CreateProject(ctx, "p", "ru", "be")
	dir := t.TempDir()
	unit := func(id, src string) string {
		return `<trans-unit id="` + id + `"><source>` + src + `</source><target state="needs-review-translation">MT ` + src + `</target></trans-unit>`
	}
	xlf := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="be"><body>` +
		unit("a", "Поговорить с Антоном о письме") +
		unit("b", "Поговорить с Антоном о письме") + // identical (e.g. another file / @male)
		unit("c", "Поговорить с Антоном о письме") +
		unit("d", "Поговорить с Олегом о письме") + // fuzzy
		unit("e", "Купить пистолет") +
		`</body></file></xliff>`
	os.WriteFile(filepath.Join(dir, "one.xlf"), []byte(xlf), 0o644)
	if _, err := importer.Import(ctx, st, qa.NewRunner(p.Settings.QA), importer.Options{ProjectID: p.ID, Root: dir}); err != nil {
		t.Fatal(err)
	}
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(New(ctx, st, config.Default(), nil, bm, nil, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(ts.Close)
	pid := "/api/projects/" + itoa(p.ID)

	ids := map[string]int64{}
	_, b := do(t, ts, "GET", pid+"/units", nil)
	var page unitsPage
	json.Unmarshal(b, &page)
	for _, r := range page.Rows {
		ids[r.Key] = r.ID
	}

	// Build the fuzzy index before the save, so the save must update it.
	do(t, ts, "GET", "/api/units/"+itoa(ids["e"])+"/tm", nil)

	_, b = do(t, ts, "PUT", "/api/units/"+itoa(ids["a"]), map[string]any{"target": "Пагаварыць з Антонам пра ліст", "state": "translated"})
	if !strings.Contains(string(b), `"duplicates":2`) {
		t.Fatalf("save should report 2 identical strings: %s", b)
	}

	var tmResp struct {
		Matches []struct {
			Score int           `json:"score"`
			Unit  int64         `json:"unit"`
			Tgt   []model.Piece `json:"tgt"`
		} `json:"matches"`
		Duplicates int `json:"duplicates"`
	}
	_, b = do(t, ts, "GET", "/api/units/"+itoa(ids["b"])+"/tm", nil)
	json.Unmarshal(b, &tmResp)
	if len(tmResp.Matches) != 1 || tmResp.Matches[0].Score != 100 || tmResp.Matches[0].Unit != ids["a"] {
		t.Errorf("exact match for b: %s", b)
	}
	_, b = do(t, ts, "GET", "/api/units/"+itoa(ids["d"])+"/tm", nil)
	tmResp.Matches = nil
	json.Unmarshal(b, &tmResp)
	if len(tmResp.Matches) != 1 || tmResp.Matches[0].Score != 80 {
		t.Errorf("fuzzy match for d (1 of 5 words differs): %s", b)
	}

	// The "same source" filter lists every identical string, itself included.
	_, b = do(t, ts, "GET", pid+"/units?same="+itoa(ids["a"]), nil)
	var same unitsPage
	json.Unmarshal(b, &same)
	if len(same.Rows) != 3 {
		t.Errorf("same-source filter: %s", b)
	}
	if _, b := do(t, ts, "GET", pid+"/count?same="+itoa(ids["a"]), nil); !strings.Contains(string(b), `"count":3`) {
		t.Errorf("same-source count: %s", b)
	}

	// Propagate to the two identical strings.
	_, b = do(t, ts, "POST", "/api/units/"+itoa(ids["a"])+"/propagate", nil)
	if !strings.Contains(string(b), `"changed":2`) {
		t.Errorf("propagate = %s", b)
	}
	_, b = do(t, ts, "GET", "/api/units/"+itoa(ids["c"]), nil)
	if !strings.Contains(string(b), `"state":"translated"`) || !strings.Contains(string(b), `"origin":"tm"`) {
		t.Errorf("c after propagate: %s", b)
	}

	// Fill job: send b and c back to MT, then fill from TM over the project.
	for _, k := range []string{"b", "c"} {
		do(t, ts, "POST", pid+"/bulk?in=key&q="+k, map[string]any{"state": "mt"})
	}
	res, b := do(t, ts, "POST", pid+"/tm/fill", nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("fill: %d %s", res.StatusCode, b)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, b = do(t, ts, "GET", "/api/jobs", nil)
		if strings.Contains(string(b), `"status":"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fill job: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(b), `"filled":2`) {
		t.Errorf("fill should restore b and c from a: %s", b)
	}
}

func TestExtraLanguageAPI(t *testing.T) {
	ts, p := newServer(t)
	pid := "/api/projects/" + itoa(p.ID)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	xlf := `<?xml version="1.0"?><xliff version="1.2"><file original="x" source-language="ru" target-language="en"><body>
<trans-unit id="1"><source>Нажмите <ph id="1">{KEY}</ph></source><target>Press <ph id="1">{KEY}</ph></target></trans-unit>
</body></file></xliff>`
	os.WriteFile(filepath.Join(dir, "a", "b", "one.xlf"), []byte(xlf), 0o644)

	if res, b := do(t, ts, "POST", pid+"/langs/import", map[string]any{"dir": dir}); res.StatusCode != http.StatusAccepted {
		t.Fatalf("import: %d %s", res.StatusCode, b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := do(t, ts, "GET", pid+"/langs", nil)
		if strings.Contains(string(b), `"lang":"en","locked":true,"units":1`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("language never listed: %s", b)
		}
		time.Sleep(100 * time.Millisecond)
	}

	_, b := do(t, ts, "GET", pid+"/units?q=1&in=key", nil)
	var page unitsPage
	json.Unmarshal(b, &page)
	_, b = do(t, ts, "GET", "/api/units/"+itoa(page.Rows[0].ID)+"/langs", nil)
	if !strings.Contains(string(b), `"lang":"en"`) || !strings.Contains(string(b), `{"t":"{KEY}","c":true}`) ||
		!strings.Contains(string(b), `"outdated":false`) {
		t.Errorf("unit langs = %s", b)
	}
	if res, _ := do(t, ts, "DELETE", pid+"/langs/en", nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", res.StatusCode)
	}
	if res, _ := do(t, ts, "DELETE", pid+"/langs/en", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("delete again: %d", res.StatusCode)
	}
}
