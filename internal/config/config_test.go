package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, yaml string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bellingua.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p, false)
}

func TestDataServerToken(t *testing.T) {
	t.Setenv("BELLINGUA_DATA_TOKEN", "from-env")

	c, err := load(t, "backup:\n  data:\n    url: https://1.2.3.4:9696\n    app: bellingua\n    token: from-file\n")
	if err != nil || c.Backup.Data.Token != "from-file" {
		t.Fatalf("file token wins: %v %+v", err, c.Backup.Data)
	}

	c, err = load(t, "backup:\n  data:\n    url: https://1.2.3.4:9696\n    app: bellingua\n")
	if err != nil || c.Backup.Data.Token != "from-env" {
		t.Fatalf("env fallback: %v %+v", err, c.Backup.Data)
	}
	if c.Backup.Data.Timeout <= 0 {
		t.Error("timeout should default")
	}

	t.Setenv("BELLINGUA_DATA_TOKEN", "")
	if _, err = load(t, "backup:\n  data:\n    url: https://1.2.3.4:9696\n    app: bellingua\n"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("missing token: %v", err)
	}
}

func TestDataServerValidation(t *testing.T) {
	t.Setenv("BELLINGUA_DATA_TOKEN", "t")
	for _, y := range []string{
		"backup:\n  data:\n    app: bellingua\n",
		"backup:\n  data:\n    url: https://h:9696\n",
		"backup:\n  data:\n    url: ftp://h\n    app: bellingua\n",
	} {
		if _, err := load(t, y); err == nil {
			t.Errorf("accepted %q", y)
		}
	}
}

func TestCrowdinToken(t *testing.T) {
	t.Setenv("BELLINGUA_CROWDIN_TOKEN", "from-env")
	c, err := load(t, "crowdin:\n  token: from-file\n")
	if err != nil || c.Crowdin.Token != "from-file" {
		t.Fatalf("file token wins: %v %q", err, c.Crowdin.Token)
	}
	if c, err = load(t, "listen: 127.0.0.1:1\n"); err != nil || c.Crowdin.Token != "from-env" {
		t.Fatalf("env fallback: %v %q", err, c.Crowdin.Token)
	}
	// No config file at all still honours the environment.
	if c, err = Load(filepath.Join(t.TempDir(), "none.yaml"), true); err != nil || c.Crowdin.Token != "from-env" {
		t.Fatalf("no file: %v %q", err, c.Crowdin.Token)
	}
}
