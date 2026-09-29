package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/Thrapis/bellingua/internal/config"
)

// LocalDir is a directory target (a second disk, a synced folder, ...).
type LocalDir string

func (d LocalDir) Name() string { return "dir:" + string(d) }

func (d LocalDir) Put(_ context.Context, name string, r io.Reader) error {
	if err := os.MkdirAll(string(d), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(string(d), name+".part")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(string(d), name))
}

func (d LocalDir) List(context.Context) ([]string, error) {
	es, err := os.ReadDir(string(d))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var out []string
	for _, e := range es {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, err
}

func (d LocalDir) Get(_ context.Context, name string, w io.Writer) error {
	f, err := os.Open(filepath.Join(string(d), name))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func (d LocalDir) Remove(_ context.Context, name string) error {
	return os.Remove(filepath.Join(string(d), name))
}

func (LocalDir) Close() error { return nil }

// SFTP is a remote target reached over SSH with key authentication. The
// connection is opened lazily and reused; a broken one is re-dialled once.
type SFTP struct {
	cfg config.SFTP

	mu     sync.Mutex
	ssh    *ssh.Client
	client *sftp.Client
}

func (s *SFTP) Name() string {
	return fmt.Sprintf("sftp:%s@%s:%s", s.cfg.User, s.cfg.Host, s.cfg.Dir)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func (s *SFTP) dial() (*sftp.Client, error) {
	key, err := os.ReadFile(expandHome(s.cfg.Key))
	if err != nil {
		return nil, fmt.Errorf("ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ssh key %s: %w (passphrase-protected keys are not supported)", s.cfg.Key, err)
	}
	kh := s.cfg.KnownHosts
	if kh == "" {
		kh = "~/.ssh/known_hosts"
	}
	hostKey, err := knownhosts.New(expandHome(kh))
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w (connect once with ssh to record the VPS key)", err)
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)), &ssh.ClientConfig{
		User:            s.cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKey,
		Timeout:         20 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true))
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.ssh, s.client = conn, c
	return c, nil
}

// with runs fn with a live client, re-dialling once if the connection died.
func (s *SFTP) with(fn func(c *sftp.Client) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; ; attempt++ {
		c := s.client
		if c == nil {
			var err error
			if c, err = s.dial(); err != nil {
				return err
			}
		}
		err := fn(c)
		if err == nil || attempt > 0 || !isConnErr(err) {
			return err
		}
		s.closeLocked()
	}
}

func isConnErr(err error) bool {
	return errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

func (s *SFTP) remote(name string) string { return path.Join(s.cfg.Dir, name) }

func (s *SFTP) Put(ctx context.Context, name string, r io.Reader) error {
	return s.with(func(c *sftp.Client) error {
		if err := c.MkdirAll(s.cfg.Dir); err != nil {
			return err
		}
		tmp := s.remote(name + ".part")
		f, err := c.Create(tmp)
		if err != nil {
			return err
		}
		_, err = f.ReadFrom(ctxReader{ctx, r})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = c.PosixRename(tmp, s.remote(name))
			if err != nil {
				err = c.Rename(tmp, s.remote(name))
			}
		}
		if err != nil {
			c.Remove(tmp)
		}
		return err
	})
}

func (s *SFTP) List(context.Context) ([]string, error) {
	var out []string
	err := s.with(func(c *sftp.Client) error {
		fis, err := c.ReadDir(s.cfg.Dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		out = out[:0]
		for _, fi := range fis {
			if !fi.IsDir() {
				out = append(out, fi.Name())
			}
		}
		return nil
	})
	return out, err
}

func (s *SFTP) Get(ctx context.Context, name string, w io.Writer) error {
	return s.with(func(c *sftp.Client) error {
		f, err := c.Open(s.remote(name))
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteTo(w)
		return err
	})
}

func (s *SFTP) Remove(_ context.Context, name string) error {
	return s.with(func(c *sftp.Client) error { return c.Remove(s.remote(name)) })
}

func (s *SFTP) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	return nil
}

func (s *SFTP) closeLocked() {
	if s.client != nil {
		s.client.Close()
	}
	if s.ssh != nil {
		s.ssh.Close()
	}
	s.client, s.ssh = nil, nil
}

// ctxReader stops an upload when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
