package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// fakeCrowdin serves one tiny project (id 7) with a Belarusian target.
func fakeCrowdin(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Unauthorized"}}`)
			return
		}
		switch r.URL.Path {
		case "/api/v2/projects/7":
			fmt.Fprint(w, `{"data":{"id":7,"name":"DRG Rogue","identifier":"Drg-Rogue","sourceLanguageId":"en","targetLanguageIds":["be"]}}`)
		case "/api/v2/projects/7/files":
			fmt.Fprint(w, `{"data":[{"data":{"id":3,"path":"/main/en.xlf"}}]}`)
		case "/api/v2/projects/7/strings":
			fmt.Fprint(w, `{"data":[{"data":{"id":1,"fileId":3,"identifier":"hello","text":"Hello"}},{"data":{"id":2,"fileId":3,"identifier":"bye","text":"Bye"}}]}`)
		case "/api/v2/projects/7/languages/be/translations":
			fmt.Fprint(w, `{"data":[{"data":{"stringId":1,"text":"Прывітанне"}}]}`)
		case "/api/v2/projects/7/approvals":
			fmt.Fprint(w, `{"data":[{"data":{"stringId":1}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v2"
}

func crowdinServer(t *testing.T, token, base string) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.Crowdin.Token, cfg.Crowdin.BaseURL = token, base
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	ts := httptest.NewServer(New(ctx, st, cfg, nil, bm, nil, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func waitDone(t *testing.T, ts *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, b := do(t, ts, "GET", "/api/jobs", nil)
		var jobs []map[string]any
		if err := json.Unmarshal(b, &jobs); err != nil {
			var wrapped struct{ Jobs []map[string]any }
			if json.Unmarshal(b, &wrapped) != nil {
				t.Fatalf("jobs: %s", b)
			}
			jobs = wrapped.Jobs
		}
		for _, j := range jobs {
			if j["id"] == id && j["status"] != "running" {
				return j
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished: %s", id, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Copying a Crowdin project creates the project, links it and pulls strings
// with their states; pulling again is a no-op; the token never comes back.
func TestCrowdinCopyAndPull(t *testing.T) {
	ts := crowdinServer(t, "secret-token", fakeCrowdin(t))

	res, b := do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7})
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("copy: %d %s", res.StatusCode, b)
	}
	var job map[string]any
	json.Unmarshal(b, &job)
	pid := "/api/projects/" + itoa(int64(job["project"].(float64)))
	if j := waitDone(t, ts, job["id"].(string)); j["status"] != "done" {
		t.Fatalf("pull: %v", j)
	}

	_, b = do(t, ts, "GET", pid, nil)
	var p struct {
		Name, SourceLang, TargetLang string
		Counts                       struct{ Total, Approved, Translated int64 }
	}
	json.Unmarshal(b, &p)
	if p.Name != "DRG Rogue" || p.SourceLang != "en" || p.TargetLang != "be" || p.Counts.Total != 2 || p.Counts.Approved != 1 {
		t.Fatalf("project = %s", b)
	}

	_, b = do(t, ts, "GET", pid+"/crowdin", nil)
	var info struct {
		TokenSet bool
		Link     struct {
			RemoteID   int64
			Identifier string
			LastPull   int64
		}
	}
	json.Unmarshal(b, &info)
	if !info.TokenSet || info.Link.RemoteID != 7 || info.Link.Identifier != "drg-rogue" || info.Link.LastPull == 0 {
		t.Fatalf("link = %s", b)
	}
	if strings.Contains(string(b), "secret-token") {
		t.Error("the token leaked into an API answer")
	}

	// A second pull finds nothing new.
	res, b = do(t, ts, "POST", pid+"/crowdin/pull", nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("pull: %d %s", res.StatusCode, b)
	}
	json.Unmarshal(b, &job)
	j := waitDone(t, ts, job["id"].(string))
	r, _ := j["result"].(map[string]any)
	if j["status"] != "done" || r["Inserted"] != float64(0) || r["Updated"] != float64(0) {
		t.Fatalf("re-pull = %v", j)
	}

	// The same name is refused instead of merged into the other project.
	if res, _ := do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7}); res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate copy: %d", res.StatusCode)
	}
}

func TestCrowdinErrors(t *testing.T) {
	base := fakeCrowdin(t)
	// No token configured: a precondition failure that says what to do.
	ts := crowdinServer(t, "", base)
	res, b := do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7})
	if res.StatusCode != http.StatusPreconditionFailed || !strings.Contains(string(b), "BELLINGUA_CROWDIN_TOKEN") {
		t.Errorf("no token: %d %s", res.StatusCode, b)
	}
	// A wrong token is the user's to fix, not a server error.
	ts = crowdinServer(t, "wrong", base)
	res, b = do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "token was refused") {
		t.Errorf("bad token: %d %s", res.StatusCode, b)
	}
	// The host of a request is never trusted with the token.
	ts = crowdinServer(t, "secret-token", base)
	res, b = do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7, "organization": "evil.example.com/x"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("organization with a dot: %d %s", res.StatusCode, b)
	}
	if res, _ := do(t, ts, "POST", "/api/crowdin/projects", map[string]any{}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no remoteId: %d", res.StatusCode)
	}
	// Pulling a project that is not linked.
	_, pb := do(t, ts, "POST", "/api/projects", map[string]any{"name": "x", "sourceLang": "en", "targetLang": "be"})
	var np struct{ ID int64 }
	json.Unmarshal(pb, &np)
	if res, _ := do(t, ts, "POST", "/api/projects/"+itoa(np.ID)+"/crowdin/pull", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unlinked pull: %d", res.StatusCode)
	}
}

func TestCrowdinLookup(t *testing.T) {
	ts := crowdinServer(t, "secret-token", fakeCrowdin(t))
	_, b := do(t, ts, "POST", "/api/crowdin/projects", map[string]any{"remoteId": 7})
	var job map[string]any
	json.Unmarshal(b, &job)
	waitDone(t, ts, job["id"].(string))
	ext := []string{"Origin", "moz-extension://abc-123"}

	// Exact match, found by the identifier the extension reads from the editor URL.
	res, b := do(t, ts, "GET", "/api/crowdin/lookup?project=DRG-Rogue&target=be&text=Hello", nil, ext...)
	var got struct {
		Project lookupProject
		Matches []lookupMatch
	}
	json.Unmarshal(b, &got)
	if res.StatusCode != 200 || len(got.Matches) != 1 || got.Matches[0].Target != "Прывітанне" ||
		got.Matches[0].Score != 100 || got.Matches[0].State != model.Approved || got.Project.TargetLang != "be" {
		t.Fatalf("lookup: %d %s", res.StatusCode, b)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "moz-extension://abc-123" {
		t.Errorf("extension got no CORS header: %v", res.Header)
	}
	// An untranslated string is not offered as a translation.
	if _, b := do(t, ts, "GET", "/api/crowdin/lookup?project=drg-rogue&text=Bye", nil); !strings.Contains(string(b), `"matches":[]`) {
		t.Errorf("untranslated source matched: %s", b)
	}
	// A web page gets no CORS permission; unknown project / language are 404.
	if res, _ := do(t, ts, "GET", "/api/crowdin/lookup?project=drg-rogue&text=Hello", nil, "Origin", "https://evil.example"); res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("a web page origin was allowed")
	}
	if res, _ := do(t, ts, "GET", "/api/crowdin/lookup?project=nope&text=Hello", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project: %d", res.StatusCode)
	}
	if res, _ := do(t, ts, "GET", "/api/crowdin/lookup?project=drg-rogue&target=uk&text=Hello", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("other language: %d", res.StatusCode)
	}
	if res, _ := do(t, ts, "GET", "/api/crowdin/lookup?project=drg-rogue", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no text: %d", res.StatusCode)
	}
	// Preflight: 204 for an extension, nothing for a page.
	res, _ = do(t, ts, "OPTIONS", "/api/crowdin/lookup.json?project=drg-rogue", nil, append(ext, "Access-Control-Request-Method", "GET")...)
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Methods") != "GET" {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}
	if res, _ := do(t, ts, "OPTIONS", "/api/crowdin/lookup.json?project=drg-rogue", nil, "Origin", "https://evil.example"); res.StatusCode != http.StatusNotFound {
		t.Errorf("page preflight: %d", res.StatusCode)
	}

	// The whole vocabulary, revalidated by ETag.
	res, b = do(t, ts, "GET", "/api/crowdin/lookup.json?project=drg-rogue", nil, ext...)
	var all struct{ Entries []lookupEntry }
	json.Unmarshal(b, &all)
	if res.StatusCode != 200 || len(all.Entries) != 1 || all.Entries[0].Source != "Hello" || all.Entries[0].Key != "hello" || all.Entries[0].Path != "main/en.xlf" {
		t.Fatalf("lookup.json: %d %s", res.StatusCode, b)
	}
	tag := res.Header.Get("ETag")
	if res, _ := do(t, ts, "GET", "/api/crowdin/lookup.json?project=drg-rogue", nil, "If-None-Match", tag); res.StatusCode != http.StatusNotModified {
		t.Errorf("same ETag: %d", res.StatusCode)
	}
	// minState changes the body, so it must change the tag.
	res, b = do(t, ts, "GET", "/api/crowdin/lookup.json?project=drg-rogue&minState=approved", nil, "If-None-Match", tag)
	if res.StatusCode != 200 || res.Header.Get("ETag") == tag {
		t.Errorf("minState reused the ETag: %d %s", res.StatusCode, b)
	}
}
