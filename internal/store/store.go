// Package store owns the SQLite database.
//
// Concurrency model: one writer connection (every write is serialised through
// it, transactions start IMMEDIATE so they never deadlock on upgrade) and a
// pool of read-only connections. In WAL mode readers never wait for the
// writer, so browsing stays fast while an import or MT batch is writing.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Thrapis/bellingua/internal/qa"
)

//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2.sql
var schemaV2 string

//go:embed schema_v3.sql
var schemaV3 string

//go:embed schema_v4.sql
var schemaV4 string

//go:embed schema_v5.sql
var schemaV5 string

//go:embed schema_v6.sql
var schemaV6 string

// migration is SQL plus an optional Go step run in the same transaction.
type migration struct {
	sql  string
	post func(ctx context.Context, tx *sql.Tx) error
}

// migrations[i] upgrades user_version i to i+1. Never edit a released one;
// append a new migration instead.
var migrations = []migration{
	{sql: schemaV1, post: func(ctx context.Context, tx *sql.Tx) error { return execAll(ctx, tx, triggerDDL) }},
	{sql: schemaV2},
	{sql: schemaV3},
	{sql: schemaV4, post: backfillSourceHash},
	{sql: schemaV5},
	{sql: schemaV6},
}

// backfillSourceHash fills units.source_hash for existing rows.
func backfillSourceHash(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, source FROM units")
	if err != nil {
		return err
	}
	type pair struct {
		id   int64
		hash int64
	}
	var all []pair
	for rows.Next() {
		var id int64
		var src string
		if err := rows.Scan(&id, &src); err != nil {
			rows.Close()
			return err
		}
		all = append(all, pair{id, SourceHash(src)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, "UPDATE units SET source_hash = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range all {
		if _, err := stmt.ExecContext(ctx, p.hash, p.id); err != nil {
			return err
		}
	}
	return nil
}

// SourceHash is the value stored in units.source_hash.
func SourceHash(source string) int64 {
	h := fnv.New64a()
	h.Write([]byte(source))
	return int64(h.Sum64())
}

// Store is the database handle.
type Store struct {
	Path string
	W    *sql.DB // single writer connection
	R    *sql.DB // read-only pool

	rev atomic.Int64
}

// ErrNotFound is returned for a missing row.
var ErrNotFound = errors.New("not found")

func dsn(path string, params ...string) string {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(10000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(1)",
		"temp_store(MEMORY)",
		"mmap_size(1073741824)",
	} {
		q.Add("_pragma", p)
	}
	for i := 0; i+1 < len(params); i += 2 {
		q.Add(params[i], params[i+1])
	}
	return "file:" + strings.ReplaceAll(path, `\`, "/") + "?" + q.Encode()
}

// Open opens (creating if needed) and migrates the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, "_pragma", "cache_size(-262144)", "_txlock", "immediate"))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{Path: path, W: w}
	if err := s.migrate(ctx); err != nil {
		w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", dsn(path, "_pragma", "cache_size(-65536)", "_pragma", "query_only(1)"))
	if err != nil {
		w.Close()
		return nil, err
	}
	n := max(runtime.NumCPU(), 4)
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	r.SetConnMaxIdleTime(0)
	r.SetConnMaxLifetime(0)
	s.R = r
	s.rev.Store(time.Now().UnixNano())
	return s, nil
}

// Close checkpoints the WAL and closes both pools.
func (s *Store) Close() error {
	var errs []error
	if s.R != nil {
		errs = append(errs, s.R.Close())
	}
	_, _ = s.W.Exec("PRAGMA optimize")
	_, _ = s.W.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	errs = append(errs, s.W.Close())
	return errors.Join(errs...)
}

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.W.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := s.W.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		m := migrations[v]
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if m.post != nil {
			if err := m.post(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", v+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Rev is a counter that changes on every write; it backs HTTP ETags.
func (s *Store) Rev() int64 { return s.rev.Load() }

// Touch bumps Rev after a write made outside Write.
func (s *Store) Touch() { s.rev.Add(1) }

// Write runs fn in a write transaction and bumps Rev on success.
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.rev.Add(1)
	return nil
}

// Optimize runs the housekeeping pragmas; call it when idle.
func (s *Store) Optimize(ctx context.Context) {
	_, _ = s.W.ExecContext(ctx, "PRAGMA optimize")
	_, _ = s.W.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func execAll(ctx context.Context, db execer, stmts []string) error {
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%w\n%s", err, q)
		}
	}
	return nil
}

// errMask is the QA error bit mask as an SQL literal.
var errMask = fmt.Sprint(uint32(qa.Errors))

// Counter expressions: a unit counts only when it is not obsolete.
func counterDelta(row, sign string) string {
	c := func(cond string) string { return fmt.Sprintf("(%s.obsolete=0 AND %s)", row, cond) }
	return strings.NewReplacer("$", sign, "R", row).Replace(fmt.Sprintf(
		"total=total$(R.obsolete=0), n_mt=n_mt$%s, n_translated=n_translated$%s, n_approved=n_approved$%s, n_err=n_err$%s, n_warn=n_warn$%s",
		c(row+".state=1"), c(row+".state=2"), c(row+".state=3"),
		c(fmt.Sprintf("(%s.qa & %s)<>0", row, errMask)), c(fmt.Sprintf("(%s.qa & ~%s)<>0", row, errMask))))
}

// triggerDDL keeps files counters and the FTS index in step with units.
// Bulk import drops these and rebuilds both in one pass instead.
var triggerDDL = []string{
	`CREATE TRIGGER units_ai AFTER INSERT ON units BEGIN
		INSERT INTO units_fts(rowid, source, target) VALUES (NEW.id, NEW.source, NEW.target);
		UPDATE files SET ` + counterDelta("NEW", "+") + ` WHERE id = NEW.file_id;
	END`,
	`CREATE TRIGGER units_ad AFTER DELETE ON units BEGIN
		INSERT INTO units_fts(units_fts, rowid, source, target) VALUES ('delete', OLD.id, OLD.source, OLD.target);
		UPDATE files SET ` + counterDelta("OLD", "-") + ` WHERE id = OLD.file_id;
	END`,
	`CREATE TRIGGER units_au_text AFTER UPDATE OF source, target ON units BEGIN
		INSERT INTO units_fts(units_fts, rowid, source, target) VALUES ('delete', OLD.id, OLD.source, OLD.target);
		INSERT INTO units_fts(rowid, source, target) VALUES (NEW.id, NEW.source, NEW.target);
	END`,
	counterTriggerDDL,
}

// counterTriggerDDL keeps the file counters in step with state/QA changes.
// A large QA recheck drops it and recounts once (RecheckQA).
var counterTriggerDDL = `CREATE TRIGGER units_au_cnt AFTER UPDATE OF state, qa, obsolete ON units BEGIN
		UPDATE files SET ` + counterDelta("OLD", "-") + ` WHERE id = OLD.file_id;
		UPDATE files SET ` + counterDelta("NEW", "+") + ` WHERE id = NEW.file_id;
	END`

var dropTriggers = []string{
	"DROP TRIGGER IF EXISTS units_ai",
	"DROP TRIGGER IF EXISTS units_ad",
	"DROP TRIGGER IF EXISTS units_au_text",
	"DROP TRIGGER IF EXISTS units_au_cnt",
}

// BeginBulk drops the maintenance triggers inside tx. The caller must call
// EndBulk in the same transaction before committing.
func BeginBulk(ctx context.Context, tx *sql.Tx) error { return execAll(ctx, tx, dropTriggers) }

// EndBulk rebuilds the FTS index and the file counters of projectID from
// scratch, then restores the triggers.
func EndBulk(ctx context.Context, tx *sql.Tx, projectID int64) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO units_fts(units_fts) VALUES ('rebuild')"); err != nil {
		return fmt.Errorf("fts rebuild: %w", err)
	}
	if err := RecountFiles(ctx, tx, projectID); err != nil {
		return err
	}
	// Fresh statistics let the planner pick the small partial indexes.
	if _, err := tx.ExecContext(ctx, "ANALYZE"); err != nil {
		return err
	}
	return execAll(ctx, tx, triggerDDL)
}

// RecountFiles recomputes every file counter of a project.
func RecountFiles(ctx context.Context, db execer, projectID int64) error {
	_, err := db.ExecContext(ctx, `
		UPDATE files SET (total, n_mt, n_translated, n_approved, n_err, n_warn) = (
			SELECT count(*),
				coalesce(sum(state = 1), 0), coalesce(sum(state = 2), 0), coalesce(sum(state = 3), 0),
				coalesce(sum((qa & `+errMask+`) <> 0), 0), coalesce(sum((qa & ~`+errMask+`) <> 0), 0)
			FROM units WHERE file_id = files.id AND obsolete = 0)
		WHERE project_id = ?`, projectID)
	return err
}
