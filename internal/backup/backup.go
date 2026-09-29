// Package backup snapshots the database and ships it to backup targets: a
// local directory and/or an SFTP server (the VPS — nothing but an SSH account
// is needed there).
//
// A snapshot is taken with VACUUM INTO on a separate connection, which in WAL
// mode neither blocks nor is blocked by the editor's writes, then compressed
// with zstd and uploaded under a temporary name and renamed, so a target
// never holds a partial file. The newest Keep snapshots are retained.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/store"
)

const (
	prefix = "bellingua-"
	suffix = ".db.zst"
	stamp  = "20060102-150405"
)

// Target is a place backups go to.
type Target interface {
	Name() string
	Put(ctx context.Context, name string, r io.Reader) error
	List(ctx context.Context) ([]string, error) // snapshot names, any order
	Get(ctx context.Context, name string, w io.Writer) error
	Remove(ctx context.Context, name string) error
	Close() error
}

// Targets opens every configured target. The caller closes them.
func Targets(cfg config.Backup) ([]Target, error) {
	var out []Target
	if cfg.Dir != "" {
		out = append(out, LocalDir(cfg.Dir))
	}
	if cfg.SFTP != nil {
		out = append(out, &SFTP{cfg: *cfg.SFTP})
	}
	return out, nil
}

// Snapshot writes a consistent zstd-compressed copy of the database at
// dbPath to w. The full-text index is about half the database and can be
// rebuilt from the units, so it is emptied in the copy (Restore rebuilds it).
func Snapshot(ctx context.Context, dbPath string, w io.Writer) error {
	tmp, err := os.CreateTemp(filepath.Dir(dbPath), ".snapshot-*.db")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath) // VACUUM INTO wants to create the file itself
	defer os.Remove(tmpPath)

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", tmpPath)
	db.Close()
	if err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	if err := stripIndex(ctx, tmpPath); err != nil {
		return fmt.Errorf("strip index: %w", err)
	}

	src, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer src.Close()
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(0))
	if err != nil {
		return err
	}
	if _, err := io.Copy(enc, src); err != nil {
		enc.Close()
		return err
	}
	return enc.Close()
}

// stripIndex empties the FTS index of the snapshot at path and compacts it.
func stripIndex(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		"INSERT INTO units_fts(units_fts) VALUES ('delete-all')",
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('fts_stripped', '1')",
		"VACUUM",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// rebuildIndex restores the FTS index of a snapshot made by Snapshot.
func rebuildIndex(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var stripped string
	_ = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'fts_stripped'").Scan(&stripped)
	if stripped != "1" {
		return nil
	}
	for _, q := range []string{
		"INSERT INTO units_fts(units_fts) VALUES ('rebuild')",
		"DELETE FROM meta WHERE key = 'fts_stripped'",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Result is the outcome of one backup run.
type Result struct {
	Name     string            `json:"name"`
	Size     int64             `json:"size"`
	At       time.Time         `json:"at"`
	Duration time.Duration     `json:"duration"`
	Errors   map[string]string `json:"errors,omitempty"` // target -> error
}

// Run takes one snapshot and uploads it to every target, pruning old ones.
func Run(ctx context.Context, dbPath string, targets []Target, keep int) (Result, error) {
	start := time.Now()
	res := Result{Name: prefix + start.Format(stamp) + suffix, At: start}
	if len(targets) == 0 {
		return res, errors.New("backup: no target configured (set backup.dir and/or backup.sftp)")
	}
	f, err := os.CreateTemp(filepath.Dir(dbPath), ".backup-*.zst")
	if err != nil {
		return res, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := Snapshot(ctx, dbPath, f); err != nil {
		return res, err
	}
	if res.Size, err = f.Seek(0, io.SeekCurrent); err != nil {
		return res, err
	}

	var errs []error
	for _, t := range targets {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return res, err
		}
		err := t.Put(ctx, res.Name, f)
		if err == nil {
			err = prune(ctx, t, keep)
		}
		if err != nil {
			if res.Errors == nil {
				res.Errors = map[string]string{}
			}
			res.Errors[t.Name()] = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", t.Name(), err))
		}
	}
	res.Duration = time.Since(start)
	return res, errors.Join(errs...)
}

// snapshots returns t's snapshot names, oldest first.
func snapshots(ctx context.Context, t Target) ([]string, error) {
	all, err := t.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range all {
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			out = append(out, n)
		}
	}
	sort.Strings(out) // the timestamp format sorts chronologically
	return out, nil
}

func prune(ctx context.Context, t Target, keep int) error {
	names, err := snapshots(ctx, t)
	if err != nil {
		return err
	}
	for len(names) > keep {
		if err := t.Remove(ctx, names[0]); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// Latest returns the newest snapshot on t.
func Latest(ctx context.Context, t Target) (string, error) {
	names, err := snapshots(ctx, t)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%s: no snapshots", t.Name())
	}
	return names[len(names)-1], nil
}

// List returns t's snapshots, newest first.
func List(ctx context.Context, t Target) ([]string, error) {
	names, err := snapshots(ctx, t)
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return names, err
}

// Restore replaces the database at dbPath with snapshot name from t. The
// database must not be open (stop the server first). The previous database
// is kept as dbPath+".bak".
func Restore(ctx context.Context, t Target, name, dbPath string) error {
	if name == "" || name == "latest" {
		var err error
		if name, err = Latest(ctx, t); err != nil {
			return err
		}
	}
	tmpZst := dbPath + ".restore.zst"
	tmpDB := dbPath + ".restore"
	defer os.Remove(tmpZst)
	defer os.Remove(tmpDB)

	zf, err := os.Create(tmpZst)
	if err != nil {
		return err
	}
	err = t.Get(ctx, name, zf)
	if cerr := zf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if err := decompress(tmpZst, tmpDB); err != nil {
		return err
	}
	if err := integrity(ctx, tmpDB); err != nil {
		return err
	}
	if err := rebuildIndex(ctx, tmpDB); err != nil {
		return fmt.Errorf("rebuild search index: %w", err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("database seems to be in use (stop the server): %w", err)
		}
	}
	if _, err := os.Stat(dbPath); err == nil {
		if err := os.Rename(dbPath, dbPath+".bak"); err != nil {
			return fmt.Errorf("database seems to be in use (stop the server): %w", err)
		}
	}
	return os.Rename(tmpDB, dbPath)
}

func decompress(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dec, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer dec.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, dec); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func integrity(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("snapshot failed integrity check: %s", res)
	}
	return nil
}

// --- scheduler ---------------------------------------------------------------

// Status is reported to the UI.
type Status struct {
	Configured bool     `json:"configured"`
	Targets    []string `json:"targets"`
	Running    bool     `json:"running"`
	Dirty      bool     `json:"dirty"` // edits since the last successful backup
	Last       *Result  `json:"last,omitempty"`
	LastError  string   `json:"lastError,omitempty"`
	Interval   string   `json:"interval"`
}

// Manager runs backups on demand, on a timer when there were edits, and on
// shutdown. It is safe for concurrent use.
type Manager struct {
	st      *store.Store
	cfg     config.Backup
	targets []Target
	log     *slog.Logger

	mu      sync.Mutex
	running bool
	lastRev int64
	last    *Result
	lastErr string
}

// NewManager builds a manager; with no targets it only reports status.
func NewManager(st *store.Store, cfg config.Backup, log *slog.Logger) (*Manager, error) {
	targets, err := Targets(cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{st: st, cfg: cfg, targets: targets, log: log, lastRev: st.Rev()}, nil
}

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{
		Configured: len(m.targets) > 0, Running: m.running, Dirty: m.st.Rev() != m.lastRev,
		Last: m.last, LastError: m.lastErr, Interval: m.cfg.Interval.String(),
	}
	for _, t := range m.targets {
		s.Targets = append(s.Targets, t.Name())
	}
	return s
}

// ErrRunning is returned when a backup is already in progress.
var ErrRunning = errors.New("backup already running")

// Now runs a backup immediately.
func (m *Manager) Now(ctx context.Context) (Result, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return Result{}, ErrRunning
	}
	m.running = true
	rev := m.st.Rev()
	m.mu.Unlock()

	res, err := Run(ctx, m.st.Path, m.targets, m.cfg.Keep)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	if err != nil {
		m.lastErr = err.Error()
		m.log.Warn("backup failed", "err", err)
	} else {
		m.lastErr = ""
		m.lastRev = rev
		m.log.Info("backup done", "name", res.Name, "size", res.Size, "took", res.Duration.Round(time.Millisecond))
	}
	if err == nil || res.Size > 0 {
		m.last = &res
	}
	return res, err
}

// Loop backs up every Interval when there were edits, and once more when
// ctx ends (graceful shutdown). It returns after that final backup.
func (m *Manager) Loop(ctx context.Context) {
	if len(m.targets) == 0 {
		<-ctx.Done()
		return
	}
	var tick <-chan time.Time
	if m.cfg.Interval > 0 {
		t := time.NewTicker(m.cfg.Interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-tick:
			if m.Status().Dirty {
				m.Now(ctx)
			}
		case <-ctx.Done():
			if m.Status().Dirty {
				m.log.Info("backup before exit")
				fin, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				m.Now(fin)
				cancel()
			}
			return
		}
	}
}

// Close releases target connections.
func (m *Manager) Close() {
	for _, t := range m.targets {
		t.Close()
	}
}
