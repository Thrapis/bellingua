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
