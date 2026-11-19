package backup

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/config"
)

// New settings take effect without a restart: a manager that started with no
// target begins backing up on its timer once one is configured, and settings
// that cannot be applied leave the old ones in place.
func TestReconfigure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := newDB(t, "alpha")
	defer st.Close()
	m, err := NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if m.Status().Configured {
		t.Fatal("configured without a target")
	}
	done := make(chan struct{})
	go func() { m.Loop(ctx); close(done) }()

	dir := t.TempDir()
	if err := m.Reconfigure(config.Backup{Dir: dir, Interval: 50 * time.Millisecond, Keep: 3}); err != nil {
		t.Fatal(err)
	}
	if !m.Status().Configured || m.Status().Interval != "50ms" {
		t.Fatalf("status = %+v", m.Status())
	}
	if _, err := st.CreateProject(ctx, "beta", "ru", "be"); err != nil { // an edit makes it dirty
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if names, _ := List(ctx, LocalDir(dir)); len(names) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no automatic backup after Reconfigure")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Turning every target off again is a valid setting.
	if err := m.Reconfigure(config.Backup{}); err != nil || m.Status().Configured {
		t.Errorf("targets off: %v %+v", err, m.Status())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not stop")
	}
}
