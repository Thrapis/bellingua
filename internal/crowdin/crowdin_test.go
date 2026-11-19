package crowdin

import (
	"context"
	"errors"
	"fmt"
	"github.com/Thrapis/bellingua/internal/model"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL + "/api/v2", Token: "tok", Sleep: func(context.Context, time.Duration) error { return nil }}
}

func TestProjectAndAuth(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/api/v2/projects/7" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprint(w, `{"data":{"id":7,"name":"DRG","identifier":"drg-core","sourceLanguageId":"en","targetLanguageIds":["be","uk"]}}`)
	})
	p, err := c.GetProject(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identifier != "drg-core" || p.SourceLang != "en" || len(p.TargetLangs) != 2 {
		t.Fatalf("project = %+v", p)
	}
}

func TestStringsPagePlural(t *testing.T) {
	// 501 strings force a second page; the last one is a plural.
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if r.URL.Query().Get("limit") != "500" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		var items []string
		for i := off; i < min(off+500, 501); i++ {
			text := `"text ` + strconv.Itoa(i) + `"`
			if i == 500 {
				text = `{"one":"a","other":"b"}`
			}
			items = append(items, fmt.Sprintf(`{"data":{"id":%d,"fileId":3,"identifier":"k%d","text":%s,"context":"c"}}`, i, i, text))
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
	})
	got, err := c.Strings(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 501 || got[0].Text != "text 0" || got[0].Identifier != "k0" || got[0].FileID != 3 {
		t.Fatalf("got %d strings, first %+v", len(got), got[0])
	}
	if !got[500].Plural || got[499].Plural {
		t.Errorf("plural flags wrong: %+v %+v", got[499], got[500])
	}
}

func TestTranslationsAndApprovals(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/projects/7/languages/be/translations":
			fmt.Fprint(w, `{"data":[{"data":{"stringId":1,"text":"Прывітанне"}},
				{"data":{"stringId":2,"contentType":"application/vnd.crowdin.text+plural","plurals":[{"text":"x","pluralForm":"one"}]}}]}`)
		case "/api/v2/projects/7/approvals":
			if r.URL.Query().Get("languageId") != "be" {
				http.Error(w, "languageId required", 400)
				return
			}
			fmt.Fprint(w, `{"data":[{"data":{"stringId":1,"translationId":9}}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	tr, err := c.Translations(context.Background(), 7, "be")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 1 || tr[1] != "Прывітанне" {
		t.Errorf("translations = %v (the plural one must be left out)", tr)
	}
	ap, err := c.Approved(context.Background(), 7, "be")
	if err != nil || !ap[1] || len(ap) != 1 {
		t.Errorf("approved = %v, %v", ap, err)
	}
}

func TestFilesStripLeadingSlash(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"data":{"id":3,"path":"/main/en.xlf"}}]}`)
	})
	fs, err := c.Files(context.Background(), 7)
	if err != nil || len(fs) != 1 || fs[0].Path != "main/en.xlf" || fs[0].ID != 3 {
		t.Fatalf("files = %+v, %v", fs, err)
	}
}

func TestRateLimitRetried(t *testing.T) {
	calls := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":{"id":7,"name":"n"}}`)
	})
	var waited []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	if _, err := c.GetProject(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(waited) != 2 || waited[0] != time.Second {
		t.Errorf("calls %d, waited %v", calls, waited)
	}
}

func TestErrorsAreReadable(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/8") {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Unauthorized","code":401}}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"Insufficient scope","code":403}}`)
	})
	var ce *Error
	_, err := c.GetProject(context.Background(), 8)
	if !errors.As(err, &ce) || ce.Status != 401 || !strings.Contains(err.Error(), "token was refused") {
		t.Errorf("401: %v", err)
	}
	_, err = c.GetProject(context.Background(), 9)
	if err == nil || !strings.Contains(err.Error(), "Insufficient scope") {
		t.Errorf("403: %v", err)
	}
	if _, err := (&Client{}).GetProject(context.Background(), 1); err == nil || !strings.Contains(err.Error(), TokenEnv) {
		t.Errorf("no token: %v", err)
	}
}

func TestPullSnapshot(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/projects/7":
			fmt.Fprint(w, `{"data":{"id":7,"name":"DRG","identifier":"drg","sourceLanguageId":"en","targetLanguageIds":["be"]}}`)
		case "/api/v2/projects/7/files":
			fmt.Fprint(w, `{"data":[{"data":{"id":3,"path":"/a/en.xlf"}},{"data":{"id":4,"path":"/empty.xlf"}}]}`)
		case "/api/v2/projects/7/strings":
			fmt.Fprint(w, `{"data":[
				{"data":{"id":12,"fileId":3,"identifier":"","text":"No key"}},
				{"data":{"id":11,"fileId":3,"identifier":"dup","text":"One","context":"ctx"}},
				{"data":{"id":13,"fileId":3,"identifier":"dup","text":"Two"}},
				{"data":{"id":14,"fileId":3,"identifier":"pl","text":{"one":"a","other":"b"}}},
				{"data":{"id":15,"fileId":99,"identifier":"lost","text":"Lost"}}]}`)
		case "/api/v2/projects/7/languages/be/translations":
			fmt.Fprint(w, `{"data":[{"data":{"stringId":11,"text":"Адзін"}},{"data":{"stringId":13,"text":"Два"}}]}`)
		case "/api/v2/projects/7/approvals":
			fmt.Fprint(w, `{"data":[{"data":{"stringId":13}}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	snap, err := c.Pull(context.Background(), 7, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Lang != "be" || snap.Plural != 1 || snap.Missing != 1 || len(snap.Files) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	f := snap.Files[0]
	if f.Path != "a/en.xlf" || len(f.Units) != 3 {
		t.Fatalf("file = %+v", f)
	}
	// Ordered by string id; a repeated or empty identifier falls back to the id.
	want := []struct {
		key, src, tgt string
		state         model.State
	}{
		{"dup", "One", "Адзін", model.Translated},
		{"id:12", "No key", "", model.Untranslated},
		{"dup#13", "Two", "Два", model.Approved},
	}
	for i, w := range want {
		u := f.Units[i]
		if u.Key != w.key || u.Source != w.src || u.Target != w.tgt || (u.HasTarget && u.State != w.state) {
			t.Errorf("unit %d = %+v, want %+v", i, u, w)
		}
	}
	if f.Units[0].Context != "ctx" {
		t.Errorf("context lost: %+v", f.Units[0])
	}
}

func TestPullReportsTotals(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/projects/7":
			fmt.Fprint(w, `{"data":{"id":7,"name":"n","identifier":"n","sourceLanguageId":"en","targetLanguageIds":["be"]}}`)
		case "/api/v2/projects/7/languages/progress":
			if r.URL.Query().Get("languageIds") != "be" {
				t.Errorf("languageIds = %q", r.URL.Query().Get("languageIds"))
			}
			fmt.Fprint(w, `{"data":[{"data":{"languageId":"be","phrases":{"total":2,"translated":1,"approved":0}}}]}`)
		case "/api/v2/projects/7/files":
			fmt.Fprint(w, `{"data":[]}`)
		case "/api/v2/projects/7/strings":
			// Crowdin says 2, but three come: the bar must not overflow.
			fmt.Fprint(w, `{"data":[{"data":{"id":1,"fileId":3,"text":"a"}},{"data":{"id":2,"fileId":3,"text":"b"}},{"data":{"id":3,"fileId":3,"text":"c"}}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	})
	var got []string
	_, err := c.Pull(context.Background(), 7, "", func(step string, done, total int) {
		if total > 0 {
			got = append(got, fmt.Sprintf("%s %d/%d", step, done, total))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "strings 3/3" || got[1] != "translations 0/1" {
		t.Errorf("progress = %v", got)
	}
}
