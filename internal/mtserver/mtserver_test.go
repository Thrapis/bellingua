package mtserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/config"
)

// The test binary doubles as the "server": run with FAKE_SERVER set, it
// listens on LINGVANEX_HOST:LINGVANEX_PORT (or fails, when asked to).
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_SERVER") {
	case "listen":
		time.Sleep(300 * time.Millisecond) // like importing ctranslate2
		ln, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("LINGVANEX_HOST"), os.Getenv("LINGVANEX_PORT")))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(0)
			}
			c.Close()
		}
	case "crash":
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'ctranslate2'")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
}

func newServer(t *testing.T, mode, port string) *Server {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "server.py"), []byte("# stand-in"), 0o644)
	t.Setenv("FAKE_SERVER", mode)
	cfg := config.Lingvanex{
		Workdir: dir, Command: []string{os.Args[0], "-test.run=^$"},
		HealthTimeout: 10 * time.Second, StopTimeout: 5 * time.Second,
	}
	s, err := New(cfg, "http://127.0.0.1:"+port, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitState(t *testing.T, s *Server, want State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := s.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %q (%s), want %q", st.State, st.Message, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStartReadyStop(t *testing.T) {
	port := freePort(t)
	s := newServer(t, "listen", port)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Status().State; st != Starting && st != Ready {
		t.Fatalf("right after Start: %q", st)
	}
	waitState(t, s, Ready)
	s.Stop()
	if st := s.Status().State; st != Off {
		t.Errorf("after Stop: %q", st)
	}
	if reachable("127.0.0.1:"+port, 200*time.Millisecond) {
		t.Error("server still listening after Stop")
	}
	s.Stop() // idempotent
}

func TestReusesRunningServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	s := newServer(t, "crash", fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
	if err := s.Start(context.Background()); err != nil || s.Status().State != External {
		t.Fatalf("state %q err %v", s.Status().State, err)
	}
	s.Stop() // nothing of ours to stop
}

func TestCrashIsReportedWithStderr(t *testing.T) {
	s := newServer(t, "crash", freePort(t))
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, Failed)
	if !strings.Contains(st.Message, "No module named 'ctranslate2'") {
		t.Errorf("message = %q", st.Message)
	}
}

func TestMissingWorkdirAndUnmanaged(t *testing.T) {
	no := false
	s, _ := New(config.Lingvanex{Workdir: filepath.Join(t.TempDir(), "nope"), Command: []string{"x"}}, "http://127.0.0.1:"+freePort(t), slog.New(slog.DiscardHandler))
	if err := s.Start(context.Background()); err == nil || s.Status().State != Failed {
		t.Errorf("missing workdir: %v %q", err, s.Status().State)
	}
	s, _ = New(config.Lingvanex{Manage: &no}, "http://127.0.0.1:"+freePort(t), slog.New(slog.DiscardHandler))
	if err := s.Start(context.Background()); err != nil || s.Status().State != Off {
		t.Errorf("unmanaged: %v %q", err, s.Status().State)
	}
}
