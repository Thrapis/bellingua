package backup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

func (d LocalDir) Size(_ context.Context, name string) (int64, error) {
	fi, err := os.Stat(filepath.Join(string(d), name))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
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

func (s *SFTP) Size(_ context.Context, name string) (n int64, err error) {
	err = s.with(func(c *sftp.Client) error {
		fi, err := c.Stat(s.remote(name))
		if err == nil {
			n = fi.Size()
		}
		return err
	})
	return n, err
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

// DataServer is a go-data-server app reached over HTTP(S) with a bearer
// token. A PUT stores the whole blob or nothing, so no temporary name is used.
type DataServer struct {
	cfg config.DataServer

	once   sync.Once
	client *http.Client
	err    error
}

func (d *DataServer) Name() string {
	host := d.cfg.URL
	if u, err := url.Parse(d.cfg.URL); err == nil {
		host = u.Host
	}
	n := "data:" + host + "/" + d.cfg.App
	if d.cfg.Folder != "" {
		n += "/" + strings.Trim(d.cfg.Folder, "/")
	}
	return n
}

func (d *DataServer) http() (*http.Client, error) {
	d.once.Do(func() {
		c := &http.Client{Timeout: d.cfg.Timeout}
		if d.cfg.CAFile != "" {
			pem, err := os.ReadFile(expandHome(d.cfg.CAFile))
			if err != nil {
				d.err = fmt.Errorf("caFile: %w", err)
				return
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				d.err = fmt.Errorf("caFile %s: no certificates found", d.cfg.CAFile)
				return
			}
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig = &tls.Config{RootCAs: pool}
			c.Transport = tr
		}
		d.client = c
	})
	return d.client, d.err
}

// key is the object key of a snapshot name inside the folder.
func (d *DataServer) key(name string) string {
	if f := strings.Trim(d.cfg.Folder, "/"); f != "" {
		return f + "/" + name
	}
	return name
}

func (d *DataServer) url(key string) string {
	u := strings.TrimRight(d.cfg.URL, "/") + "/api/" + url.PathEscape(d.cfg.App)
	if key == "" {
		return u
	}
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return u + "/" + strings.Join(segs, "/")
}

// do sends a request and returns the response for a 2xx status; any other
// status becomes an error carrying the server's message.
func (d *DataServer) do(ctx context.Context, method, target string, body io.Reader, size int64) (*http.Response, error) {
	c, err := d.http()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	if body != nil && size >= 0 {
		req.ContentLength = size
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	err = fmt.Errorf("%s %s: %s: %s", method, path.Base(target), resp.Status, strings.TrimSpace(string(msg)))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		err = fmt.Errorf("%w (the snapshot exceeds the server's DATA_MAX_BODY; raise it on the server)", err)
	}
	return nil, err
}

func (d *DataServer) Put(ctx context.Context, name string, r io.Reader) error {
	size := int64(-1)
	if f, ok := r.(*os.File); ok {
		// Run hands over a temp file; a known length avoids chunked bodies.
		if st, err := f.Stat(); err == nil {
			if off, err := f.Seek(0, io.SeekCurrent); err == nil {
				size = st.Size() - off
			}
		}
	}
	resp, err := d.do(ctx, http.MethodPut, d.url(d.key(name)), r, size)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

type dataObject struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

// objects lists the keys that start with prefix.
func (d *DataServer) objects(ctx context.Context, prefix string) ([]dataObject, error) {
	resp, err := d.do(ctx, http.MethodGet, d.url("")+"?prefix="+url.QueryEscape(prefix), nil, -1)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var objs []dataObject
	if err := json.NewDecoder(resp.Body).Decode(&objs); err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}
	return objs, nil
}

func (d *DataServer) List(ctx context.Context) ([]string, error) {
	pfx := ""
	if f := strings.Trim(d.cfg.Folder, "/"); f != "" {
		pfx = f + "/"
	}
	objs, err := d.objects(ctx, pfx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range objs {
		if n, ok := strings.CutPrefix(o.Key, pfx); ok && n != "" && !strings.Contains(n, "/") {
			out = append(out, n)
		}
	}
	return out, nil
}

// Size reads the snapshot's size from the listing.
func (d *DataServer) Size(ctx context.Context, name string) (int64, error) {
	key := d.key(name)
	objs, err := d.objects(ctx, key)
	if err != nil {
		return 0, err
	}
	for _, o := range objs {
		if o.Key == key {
			return o.Size, nil
		}
	}
	return 0, os.ErrNotExist
}

func (d *DataServer) Get(ctx context.Context, name string, w io.Writer) error {
	resp, err := d.do(ctx, http.MethodGet, d.url(d.key(name)), nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (d *DataServer) Remove(ctx context.Context, name string) error {
	resp, err := d.do(ctx, http.MethodDelete, d.url(d.key(name)), nil, -1)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil // already gone
		}
		return err
	}
	return resp.Body.Close()
}

func (d *DataServer) Close() error {
	if d.client != nil {
		d.client.CloseIdleConnections()
	}
	return nil
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
