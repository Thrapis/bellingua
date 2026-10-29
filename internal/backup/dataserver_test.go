package backup

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/config"
)

// fakeDataServer mimics the parts of go-data-server the target uses: bearer
// auth per app, PUT/GET/DELETE of /api/{app}/{key...} and a JSON listing.
type fakeDataServer struct {
	mu     sync.Mutex
	blobs  map[string][]byte
	token  string
	max    int64
	putLen int64 // Content-Length of the last PUT (-1 when chunked)
}

func (f *fakeDataServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/bk")
	if !ok {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimPrefix(rest, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && key == "":
		type obj struct {
			Key  string `json:"key"`
			Size int    `json:"size"`
		}
		out := []obj{}
		for k, v := range f.blobs {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				out = append(out, obj{k, len(v)})
			}
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet:
		b, ok := f.blobs[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	case r.Method == http.MethodPut:
		f.putLen = r.ContentLength
		if f.max > 0 && r.ContentLength > f.max {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return // aborted upload: nothing stored
		}
		f.blobs[key] = b
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete:
		if _, ok := f.blobs[key]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(f.blobs, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "bad", http.StatusMethodNotAllowed)
	}
}

func newDataServer(t *testing.T, folder string) (*fakeDataServer, *DataServer) {
	t.Helper()
	f := &fakeDataServer{blobs: map[string][]byte{}, token: "secret-token"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &DataServer{cfg: config.DataServer{URL: srv.URL, App: "bk", Token: f.token, Folder: folder, Timeout: time.Minute}}
}

func TestDataServer(t *testing.T) {
	f, target := newDataServer(t, "")
	roundTrip(t, target)
	if len(f.blobs) != 1 {
		t.Errorf("server holds %d blobs, want 1", len(f.blobs))
	}
}

func TestDataServerFolder(t *testing.T) {
	f, target := newDataServer(t, "laptop")
	roundTrip(t, target)
	for k := range f.blobs {
		if !strings.HasPrefix(k, "laptop/bellingua-") {
			t.Errorf("blob key %q is not under the folder", k)
		}
	}
}

func TestDataServerErrors(t *testing.T) {
	ctx := context.Background()
	f, target := newDataServer(t, "")

	if strings.Contains(target.Name(), f.token) {
		t.Errorf("Name leaks the token: %s", target.Name())
	}

	bad := &DataServer{cfg: target.cfg}
	bad.cfg.Token = "wrong"
	if err := bad.Put(ctx, "bellingua-x.db.zst", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: %v", err)
	}

	f.max = 3
	err := target.Put(ctx, "bellingua-x.db.zst", strings.NewReader("12345678"))
	if err == nil || !strings.Contains(err.Error(), "DATA_MAX_BODY") {
		t.Errorf("413 should name DATA_MAX_BODY: %v", err)
	}
	if names, _ := target.List(ctx); len(names) != 0 {
		t.Errorf("failed upload left %v", names)
	}

	f.max = 0
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := target.Put(cctx, "bellingua-y.db.zst", strings.NewReader("data")); err == nil {
		t.Error("cancelled Put succeeded")
	}
	if names, _ := target.List(ctx); len(names) != 0 {
		t.Errorf("cancelled upload left %v", names)
	}

	if err := target.Remove(ctx, "bellingua-missing.db.zst"); err != nil {
		t.Errorf("removing a missing snapshot: %v", err)
	}
}

// A manager started after earlier backups reports the newest snapshot of its
// targets as "last", with the stored size, instead of "No backup yet".
func TestManagerLoadsLastFromTargets(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	addUnit(t, st, "first", "Привет, мир")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	f, target := newDataServer(t, "")
	res, err := Run(ctx, st.Path, []Target{target}, 5)
	if err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(st, config.Backup{Data: &target.cfg}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Status().Last != nil {
		t.Fatal("nothing should be known before the targets are asked")
	}
	m.loadLast(ctx)
	last := m.Status().Last
	if last == nil || last.Name != res.Name {
		t.Fatalf("last = %+v", last)
	}
	if want := int64(len(f.blobs[res.Name])); last.Size != want || want == 0 {
		t.Errorf("size = %d, want %d", last.Size, want)
	}
	if last.At.IsZero() || time.Since(last.At) > time.Minute {
		t.Errorf("at = %v", last.At)
	}
}
