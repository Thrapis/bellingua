package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const patchBase = `# my settings
db: bellingua.db            # SQLite file
listen: 127.0.0.1:7070

backup:
  interval: 15m             # automatic snapshot
  keep: 20
  # dir: D:/Backups/bellingua
  # sftp:
  #   host: vps.example.com
`

func patchFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bellingua.yaml")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// Patch changes only the keys it is given: comments, other keys and their
// order survive, and the result loads.
func TestPatchKeepsComments(t *testing.T) {
	p := patchFile(t, patchBase)
	err := Patch(p, map[string]any{
		"backup.interval": "30m",
		"backup.dir":      "D:/Backups/mine",
		"backup.projects": []string{"alpha", "123"},
		"crowdin.baseUrl": "https://acme.api.crowdin.com/api/v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	out := string(b)
	for _, want := range []string{"# my settings", "# SQLite file", "# automatic snapshot", "# dir: D:/Backups/bellingua", "keep: 20", "listen: 127.0.0.1:7070"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	c, err := Load(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backup.Interval != 30*time.Minute || c.Backup.Dir != "D:/Backups/mine" || c.Backup.Keep != 20 ||
		!slices.Equal(c.Backup.Projects, []string{"alpha", "123"}) ||
		c.Crowdin.BaseURL != "https://acme.api.crowdin.com/api/v2" || c.Listen != "127.0.0.1:7070" {
		t.Errorf("config = %+v", c)
	}
}

// An empty value removes the key (and a parent left empty), so the default applies again.
func TestPatchRemovesKeys(t *testing.T) {
	p := patchFile(t, patchBase)
	if err := Patch(p, map[string]any{"backup.dir": "x", "backup.projects": []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := Patch(p, map[string]any{"backup.dir": "", "backup.projects": []string{}, "backup.keep": nil}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backup.Dir != "" || len(c.Backup.Projects) != 0 || c.Backup.Keep != 20 { // keep falls back to its default
		t.Errorf("backup = %+v", c.Backup)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "projects:") || strings.Contains(string(b), "keep:") {
		t.Errorf("keys were not removed:\n%s", b)
	}
	// A parent that became empty goes too.
	if err := Patch(p, map[string]any{"crowdin.baseUrl": "https://x.api.crowdin.com/api/v2"}); err != nil {
		t.Fatal(err)
	}
	if err := Patch(p, map[string]any{"crowdin.baseUrl": ""}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "crowdin") {
		t.Errorf("empty section stayed:\n%s", b)
	}
}

// A change that would make the file invalid is refused and the file is untouched.
func TestPatchRefusesInvalid(t *testing.T) {
	p := patchFile(t, patchBase)
	t.Setenv("BELLINGUA_DATA_TOKEN", "")
	err := Patch(p, map[string]any{"backup.data.url": "https://1.2.3.4:9696", "backup.data.app": "x"})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != patchBase {
		t.Errorf("file changed despite the error:\n%s", b)
	}
	if left, _ := filepath.Glob(p + ".*"); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

// No file yet, or one holding only comments: Patch creates the content.
func TestPatchNewAndCommentOnlyFile(t *testing.T) {
	for _, body := range []string{"", "# nothing yet\n"} {
		p := patchFile(t, body)
		if err := Patch(p, map[string]any{"backup.keep": 5, "logLevel": "debug"}); err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		c, err := Load(p, false)
		if err != nil || c.Backup.Keep != 5 || c.LogLevel != "debug" {
			t.Errorf("body %q: %+v %v", body, c, err)
		}
		if body != "" {
			if b, _ := os.ReadFile(p); !strings.Contains(string(b), "# nothing yet") {
				t.Errorf("comment lost:\n%s", b)
			}
		}
	}
}

// Numbers-looking names stay strings, so a project called 123 is not turned into an int.
func TestPatchQuotesNumericStrings(t *testing.T) {
	p := patchFile(t, "")
	if err := Patch(p, map[string]any{"backup.projects": []string{"123", "true"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, false)
	if err != nil || !slices.Equal(c.Backup.Projects, []string{"123", "true"}) {
		t.Errorf("%+v %v", c.Backup.Projects, err)
	}
}
