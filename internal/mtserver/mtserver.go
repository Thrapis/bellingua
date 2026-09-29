// Package mtserver runs the local Lingvanex translation server
// (third_party/lingvanex-server/server.py) for "bellingua serve": it starts
// the Python process in the background, waits for its port, streams its
// output to the log, reports its state to the UI and stops it (with its
// children) on shutdown.
//
// Start does not block (the editor is usable while models load), the state
// is observable, the tail of stderr is kept so a failure (say, a missing
// Python package) can be shown in the UI, and one goroutine owns
// cmd.Wait, so a crash and a Stop never race over it.
package mtserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Thrapis/bellingua/internal/config"
)

// State of the server.
type State string

const (
	Off      State = "off"      // not managed and nothing listening
	Starting State = "starting" // process started, port not open yet
	Ready    State = "ready"    // our process is serving
	External State = "external" // something else already listens on the port
	Failed   State = "failed"   // could not start, or exited
)

// Status is reported to the UI.
type Status struct {
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
	Addr    string `json:"addr"`
}

// Server supervises one server process.
type Server struct {
	cfg  config.Lingvanex
	log  *slog.Logger
	addr string

	mu      sync.Mutex
	state   State
	msg     string
	cmd     *exec.Cmd
	exited  chan struct{} // closed by the waiter goroutine
	stopped bool
	tail    []string // last stderr lines
}

// New prepares a supervisor for the server at providerURL (the lingvanex
// provider's URL). It does not start anything.
func New(cfg config.Lingvanex, providerURL string, log *slog.Logger) (*Server, error) {
	u, err := url.Parse(providerURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("lingvanex: bad provider url %q", providerURL)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "80"
	}
	return &Server{cfg: cfg, log: log, addr: net.JoinHostPort(host, port), state: Off}, nil
}

// Status returns the current state.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{State: s.state, Message: s.msg, Addr: s.addr}
}

func (s *Server) set(st State, msg string) {
	s.mu.Lock()
	s.state, s.msg = st, msg
	s.mu.Unlock()
}

// Start launches the server unless one already answers on the port. It
// returns quickly; readiness is reported through Status.
func (s *Server) Start(ctx context.Context) error {
	if reachable(s.addr, 300*time.Millisecond) {
		s.log.Info("lingvanex: using the server already running", "addr", s.addr)
		s.set(External, "")
		return nil
	}
	if !s.cfg.Managed() {
		s.set(Off, "not running at "+s.addr+" and lingvanex.manage is false")
		return nil
	}
	dir, err := resolveWorkdir(s.cfg.Workdir)
	if err != nil {
		s.set(Failed, err.Error())
		return err
	}
	if len(s.cfg.Command) == 0 {
		s.set(Failed, "lingvanex.command is empty")
		return errors.New("lingvanex: command is empty")
	}
	host, port, _ := net.SplitHostPort(s.addr)

	cmd := exec.Command(s.cfg.Command[0], s.cfg.Command[1:]...)
	cmd.Dir = dir
	// UTF-8 through the pipes (Windows defaults to the ANSI code page and the
	// server crashes on the first Cyrillic line it prints), and our address.
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8",
		"LINGVANEX_HOST="+host, "LINGVANEX_PORT="+port)
	configureProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	s.log.Info("lingvanex: starting server", "command", strings.Join(s.cfg.Command, " "), "workdir", dir, "addr", s.addr)
	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("cannot run %q: %v (is Python installed? see %s/README.md)", s.cfg.Command[0], err, s.cfg.Workdir)
		s.set(Failed, msg)
		return errors.New("lingvanex: " + msg)
	}

	bindToParent(cmd, s.log)

	exited := make(chan struct{})
	s.mu.Lock()
	s.cmd, s.exited, s.state, s.msg, s.stopped = cmd, exited, Starting, "loading", false
	s.mu.Unlock()

	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); s.pipe("stdout", stdout) }()
	go func() { defer pipes.Done(); s.pipe("stderr", stderr) }()

	// The only cmd.Wait: marks the server failed if it dies on its own.
	go func() {
		pipes.Wait() // Wait must not run before the pipes are drained
		err := cmd.Wait()
		close(exited)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopped {
			return
		}
		msg := "the server exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		if len(s.tail) > 0 {
			msg += "\n" + strings.Join(s.tail, "\n")
		}
		s.state, s.msg = Failed, msg
		s.log.Warn("lingvanex: server exited", "err", err)
	}()

	go func() {
		deadline := time.Now().Add(s.cfg.HealthTimeout)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return
			case <-exited:
				return
			case <-time.After(500 * time.Millisecond):
			}
			if reachable(s.addr, time.Second) {
				s.set(Ready, "")
				s.log.Info("lingvanex: server ready", "addr", s.addr)
				return
			}
		}
		s.set(Failed, fmt.Sprintf("the server did not open %s within %s", s.addr, s.cfg.HealthTimeout))
		s.log.Warn("lingvanex: server did not come up", "timeout", s.cfg.HealthTimeout)
	}()
	return nil
}

// Stop terminates the process tree we started. Safe to call more than once
// and when nothing was started.
func (s *Server) Stop() {
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	s.stopped, s.cmd = true, nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	s.log.Info("lingvanex: stopping server", "pid", cmd.Process.Pid)
	stopProcess(cmd, exited, s.cfg.StopTimeout, s.log)
	s.set(Off, "stopped")
}

func (s *Server) pipe(stream string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		s.log.Debug("lingvanex", "stream", stream, "line", line)
		if stream == "stderr" {
			s.mu.Lock()
			s.tail = append(s.tail, line)
			if len(s.tail) > 8 {
				s.tail = s.tail[len(s.tail)-8:]
			}
			s.mu.Unlock()
		}
	}
}

func reachable(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// resolveWorkdir finds the server folder: as given, or (for a relative path)
// in the working directory, then next to the executable.
func resolveWorkdir(dir string) (string, error) {
	candidates := []string{dir}
	if !filepath.IsAbs(dir) {
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), dir))
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "server.py")); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("lingvanex: server.py not found in %q (set lingvanex.workdir)", dir)
}
