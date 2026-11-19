package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/store"
)

const settingsYAML = `# my bellingua
logLevel: info   # how chatty
backup:
  interval: 15m  # automatic
  keep: 20
crowdin:
  token: secret-crowdin-token
`

func settingsServer(t *testing.T) (*httptest.Server, string, *store.Store, *backup.Manager) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"alpha", "beta"} {
		if _, err := st.CreateProject(ctx, n, "en", "be"); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "bellingua.yaml")
	if err := os.WriteFile(path, []byte(settingsYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	bm, err := backup.NewManager(st, cfg.Backup, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ctx, st, cfg, nil, bm, nil, slog.New(slog.DiscardHandler))
	s.SetConfigPath(path)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, path, st, bm
}

type settingsResp struct {
	Path    string
	General struct{ LogLevel string }
	Backup  struct {
		Interval string
		Keep     int
		Dir      string
		Projects []string
	}
	Crowdin         struct{ TokenSet bool }
	Projects        []string
	RestartRequired []string
}

func getSettings(t *testing.T, ts *httptest.Server) settingsResp {
	t.Helper()
	res, b := do(t, ts, "GET", "/api/settings", nil)
	if res.StatusCode != 200 {
		t.Fatalf("GET settings: %d %s", res.StatusCode, b)
	}
	var o settingsResp
	json.Unmarshal(b, &o)
	return o
}

func TestSettingsReadHidesSecrets(t *testing.T) {
	ts, _, _, _ := settingsServer(t)
	res, b := do(t, ts, "GET", "/api/settings", nil)
	if res.StatusCode != 200 || strings.Contains(string(b), "secret-crowdin-token") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	o := getSettings(t, ts)
	if o.Backup.Interval != "15m0s" || o.Backup.Keep != 20 || !o.Crowdin.TokenSet || !slices.Equal(o.Projects, []string{"alpha", "beta"}) {
		t.Errorf("settings = %+v", o)
	}
}

// Saving writes the file (comments kept) and takes effect at once: the backup
// manager has the new target, and the project choice is remembered.
func TestSettingsSaveAppliesLive(t *testing.T) {
	ts, path, _, bm := settingsServer(t)
	dir := t.TempDir()
	if bm.Status().Configured {
		t.Fatal("configured before saving")
	}
	res, b := do(t, ts, "PUT", "/api/settings", map[string]any{
		"backup": map[string]any{"dir": dir, "interval": "1h", "projects": []string{"beta"}},
	})
	if res.StatusCode != 200 {
		t.Fatalf("PUT: %d %s", res.StatusCode, b)
	}
	if !bm.Status().Configured || bm.Status().Interval != "1h0m0s" {
		t.Errorf("manager not reconfigured: %+v", bm.Status())
	}
	o := getSettings(t, ts)
	if o.Backup.Dir != dir || !slices.Equal(o.Backup.Projects, []string{"beta"}) || o.Backup.Keep != 20 {
		t.Errorf("after save = %+v", o.Backup)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# my bellingua", "# how chatty", "# automatic", "secret-crowdin-token"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("file lost %q:\n%s", want, raw)
		}
	}
	// Clearing the choice means every project again.
	if res, b := do(t, ts, "PUT", "/api/settings", map[string]any{"backup": map[string]any{"projects": []string{}}}); res.StatusCode != 200 {
		t.Fatalf("clear: %d %s", res.StatusCode, b)
	}
	if o := getSettings(t, ts); len(o.Backup.Projects) != 0 {
		t.Errorf("projects = %v", o.Backup.Projects)
	}
}

func TestSettingsRefusesBadValues(t *testing.T) {
	ts, path, _, bm := settingsServer(t)
	before, _ := os.ReadFile(path)
	for name, body := range map[string]map[string]any{
		"interval":   {"backup": map[string]any{"interval": "often"}},
		"level":      {"general": map[string]any{"logLevel": "loud"}},
		"crowdin":    {"crowdin": map[string]any{"baseUrl": "http://api.crowdin.com/api/v2"}},
		"half sftp":  {"backup": map[string]any{"sftp": map[string]any{"host": "vps.example.com"}}},
		"data token": {"backup": map[string]any{"data": map[string]any{"url": "https://1.2.3.4:9696", "app": "x"}}},
		"nothing":    {},
	} {
		t.Setenv("BELLINGUA_DATA_TOKEN", "")
		res, b := do(t, ts, "PUT", "/api/settings", body)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.StatusCode, b)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("a refused change altered the file:\n%s", after)
	}
	if bm.Status().Configured {
		t.Error("manager changed by a refused save")
	}
}

func TestSettingsLogLevelNeedsRestart(t *testing.T) {
	ts, _, _, _ := settingsServer(t)
	res, b := do(t, ts, "PUT", "/api/settings", map[string]any{"general": map[string]any{"logLevel": "debug"}})
	var o settingsResp
	json.Unmarshal(b, &o)
	if res.StatusCode != 200 || !slices.Equal(o.RestartRequired, []string{"logLevel"}) {
		t.Errorf("%d %s", res.StatusCode, b)
	}
}

// Switching a target off removes its section, so the rest of the file stays valid.
func TestSettingsTurnTargetsOff(t *testing.T) {
	ts, path, _, bm := settingsServer(t)
	t.Setenv("BELLINGUA_DATA_TOKEN", "")
	if err := config.Patch(path, map[string]any{"backup.data.url": "https://1.2.3.4:9696", "backup.data.app": "x", "backup.data.token": "tok", "backup.dir": ""}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path, false)
	if err := bm.Reconfigure(cfg.Backup); err != nil || !bm.Status().Configured {
		t.Fatalf("setup: %v", err)
	}
	// Writing the file directly bypasses the server's view; read it back through the page.
	res, b := do(t, ts, "PUT", "/api/settings", map[string]any{"backup": map[string]any{"data": map[string]any{}, "sftp": map[string]any{}}})
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	if bm.Status().Configured {
		t.Error("the data target is still configured")
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "data:") || strings.Contains(string(raw), "token: tok\n") {
		t.Errorf("section left behind:\n%s", raw)
	}
}
