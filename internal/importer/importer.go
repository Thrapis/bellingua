// Package importer loads a tree of XLIFF 1.2 or Crowdin CSV files into a
// project. Files are parsed by a pool of workers while a single goroutine
// writes everything in one transaction with the maintenance triggers dropped;
// the FTS index and file counters are rebuilt once at the end.
//
// Re-importing into a project merges by (file path, unit key):
//   - new keys are inserted, keys missing from an imported file become obsolete
//     (hidden, never deleted), a key that reappears is revived;
//   - a changed source is stored and flagged "changed"; a human translation
//     (translated/approved) is kept and demoted to translated for review,
//     a machine one is replaced by the incoming target;
//   - with an unchanged source, incoming targets only replace machine or
//     missing translations — human work is never overwritten.
//
// Files of the project that are absent from the imported tree are untouched,
// so importing a sub-folder is safe.
package importer

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Thrapis/bellingua/internal/formats/crowdincsv"
	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

// Formats accepted by Options.Format.
const (
	XLIFF      = "xliff"
	CrowdinCSV = "crowdin-csv"
)

// Options configures an import.
type Options struct {
	ProjectID int64
	Root      string // directory to import
	Format    string // xliff | crowdin-csv | "" = by file extension
	Workers   int    // parser goroutines; 0 = NumCPU
	Progress  func(done, total int)
	Log       *slog.Logger
}

// Stats summarises an import.
type Stats struct {
	Files         int
	Units         int
	Inserted      int
	Updated       int
	SourceChanged int
	Obsolete      int
	Problems      int // targets rejected by the XLIFF reader (broken codes)
	Duration      time.Duration
}

type unit struct {
	key, context string
	src, tgt     []model.Piece // tgt nil = no translation
	state        model.State
	mask         qa.Check
}

type parsed struct {
	path, format, original string
	units                  []unit
	problems               int
	err                    error
}

// Import runs an import.
func Import(ctx context.Context, st *store.Store, runner *qa.Runner, opt Options) (Stats, error) {
	start := time.Now()
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	paths, err := listFiles(opt.Root, opt.Format)
	if err != nil {
		return Stats{}, err
	}
	if len(paths) == 0 {
		return Stats{}, fmt.Errorf("import: no %s files under %s", nonEmpty(opt.Format, "xliff/csv"), opt.Root)
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Parsers: a bounded pipeline so memory stays flat on huge trees.
	jobs := make(chan string)
	results := make(chan parsed, workers*2)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				r := parseFile(opt, p, runner)
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range paths {
			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var stats Stats
	err = st.Write(ctx, func(tx *sql.Tx) error {
		w, err := newWriter(ctx, tx, opt.ProjectID, runner)
		if err != nil {
			return err
		}
		defer w.close()
		// A fresh project is loaded in bulk mode (no triggers, one rebuild of
		// FTS and counters at the end). A re-import touches few rows, so the
		// triggers stay on and only the changed rows pay for maintenance.
		bulk := !w.hasUnits()
		if bulk {
			if err := store.BeginBulk(ctx, tx); err != nil {
				return err
			}
		}
		// Create file rows up front, in path order, so file ids (and with
		// them the list order) follow the tree.
		if err := w.ensureFiles(paths, opt.Format); err != nil {
			return err
		}
		done := 0
		for r := range results {
			if r.err != nil {
				return r.err
			}
			if err := w.writeFile(r, &stats); err != nil {
				return fmt.Errorf("%s: %w", r.path, err)
			}
			done++
			if opt.Progress != nil {
				opt.Progress(done, len(paths))
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !bulk {
			return nil
		}
		log.Info("import: rebuilding index")
		return store.EndBulk(ctx, tx, opt.ProjectID)
	})
	stats.Duration = time.Since(start)
	return stats, err
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func formatOf(p, forced string) string {
	if forced != "" {
		return forced
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".xlf", ".xliff":
		return XLIFF
	case ".csv":
		return CrowdinCSV
	}
	return ""
}

// listFiles returns the slash-separated relative paths to import, sorted.
func listFiles(root, format string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f := formatOf(rel, "")
		if f != "" && (format == "" || format == f) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func parseFile(opt Options, rel string, runner *qa.Runner) parsed {
	r := parsed{path: rel, format: formatOf(rel, opt.Format)}
	fh, err := os.Open(filepath.Join(opt.Root, filepath.FromSlash(rel)))
	if err != nil {
		r.err = err
		return r
	}
	defer fh.Close()
	switch r.format {
	case XLIFF:
		f, problems, err := xliff.Read(fh)
		if err != nil {
			r.err = fmt.Errorf("%s: %w", rel, err)
			return r
		}
		r.original, r.problems = f.Original, len(problems)
		r.units = make([]unit, len(f.Units))
		for i, u := range f.Units {
			x := unit{key: u.ID, context: u.Note, src: u.Source, tgt: u.Target}
			if u.Target != nil {
				x.state = model.FromXLIFFState(u.State)
			}
			r.units[i] = x
		}
	case CrowdinCSV:
		rows, err := crowdincsv.Read(fh)
		if err != nil {
			r.err = fmt.Errorf("%s: %w", rel, err)
			return r
		}
		r.units = make([]unit, len(rows))
		for i, row := range rows {
			x := unit{key: row.ID, context: row.Context, src: []model.Piece{{Text: row.Source}}}
			if row.Translation != "" {
				x.tgt, x.state = []model.Piece{{Text: row.Translation}}, model.MT
			}
			r.units[i] = x
		}
	default:
		r.err = fmt.Errorf("%s: unknown format %q", rel, r.format)
		return r
	}
	for i := range r.units {
		u := &r.units[i]
		u.mask = runner.Mask(qa.Unit{Source: u.src, Target: u.tgt, State: u.state})
	}
	return r
}

// --- writing -----------------------------------------------------------------

type writer struct {
	ctx       context.Context
	tx        *sql.Tx
	projectID int64
	runner    *qa.Runner
	now       int64
	fileIDs   map[string]int64
	existing  map[int64]bool // files that already had units before this import
	ins, upd  *sql.Stmt
}

func newWriter(ctx context.Context, tx *sql.Tx, projectID int64, runner *qa.Runner) (*writer, error) {
	w := &writer{ctx: ctx, tx: tx, projectID: projectID, runner: runner, now: time.Now().Unix(),
		fileIDs: map[string]int64{}, existing: map[int64]bool{}}
	rows, err := tx.QueryContext(ctx, `SELECT f.id, f.path, EXISTS (SELECT 1 FROM units u WHERE u.file_id = f.id)
		FROM files f WHERE f.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var p string
		var has bool
		if err := rows.Scan(&id, &p, &has); err != nil {
			rows.Close()
			return nil, err
		}
		w.fileIDs[p] = id
		w.existing[id] = has
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if w.ins, err = tx.PrepareContext(ctx, `INSERT INTO units (project_id, file_id, ord, key, source, source_pieces,
		target, target_pieces, context, state, qa, updated_at, source_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return nil, err
	}
	if w.upd, err = tx.PrepareContext(ctx, `UPDATE units SET ord = ?, source = ?, source_pieces = ?, target = ?,
		target_pieces = ?, context = ?, state = ?, qa = ?, changed = ?, obsolete = 0, updated_at = ?, source_hash = ? WHERE id = ?`); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *writer) hasUnits() bool {
	for _, has := range w.existing {
		if has {
			return true
		}
	}
	return false
}

func (w *writer) close() {
	if w.ins != nil {
		w.ins.Close()
	}
	if w.upd != nil {
		w.upd.Close()
	}
}

func (w *writer) ensureFiles(paths []string, format string) error {
	stmt, err := w.tx.PrepareContext(w.ctx, `INSERT INTO files (project_id, path, dir, format) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range paths {
		if _, ok := w.fileIDs[p]; ok {
			continue
		}
		dir := path.Dir(p)
		if dir == "." {
			dir = ""
		}
		res, err := stmt.ExecContext(w.ctx, w.projectID, p, dir, formatOf(p, format))
		if err != nil {
			return err
		}
		if w.fileIDs[p], err = res.LastInsertId(); err != nil {
			return err
		}
	}
	return nil
}

func plainOrNil(ps []model.Piece) any {
	if ps == nil {
		return nil
	}
	return model.Plain(ps)
}

func (w *writer) writeFile(r parsed, stats *Stats) error {
	fileID := w.fileIDs[r.path]
	if _, err := w.tx.ExecContext(w.ctx, "UPDATE files SET original = ?, format = ? WHERE id = ?", r.original, r.format, fileID); err != nil {
		return err
	}
	stats.Files++
	stats.Units += len(r.units)
	stats.Problems += r.problems
	if !w.existing[fileID] {
		for i, u := range r.units {
			if _, err := w.ins.ExecContext(w.ctx, w.projectID, fileID, i, u.key, model.Plain(u.src), model.EncodePieces(u.src),
				plainOrNil(u.tgt), model.EncodePieces(u.tgt), u.context, u.state, u.mask, w.now, store.SourceHash(model.Plain(u.src))); err != nil {
				return err
			}
		}
		stats.Inserted += len(r.units)
		return nil
	}
	return w.merge(fileID, r, stats)
}

type existingUnit struct {
	id                int64
	ord               int64
	source            string
	target            sql.NullString
	targetPieces      []byte
	context           string
	state             model.State
	mask              qa.Check
	changed, obsolete bool
	seen              bool
}

func (w *writer) merge(fileID int64, r parsed, stats *Stats) error {
	rows, err := w.tx.QueryContext(w.ctx, `SELECT id, key, ord, source, target, target_pieces, context, state, qa, changed, obsolete
		FROM units WHERE file_id = ?`, fileID)
	if err != nil {
		return err
	}
	old := map[string]*existingUnit{}
	for rows.Next() {
		var key string
		e := &existingUnit{}
		if err := rows.Scan(&e.id, &key, &e.ord, &e.source, &e.target, &e.targetPieces, &e.context, &e.state, &e.mask, &e.changed, &e.obsolete); err != nil {
			rows.Close()
			return err
		}
		old[key] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for i, u := range r.units {
		e, ok := old[u.key]
		if !ok {
			if _, err := w.ins.ExecContext(w.ctx, w.projectID, fileID, i, u.key, model.Plain(u.src), model.EncodePieces(u.src),
				plainOrNil(u.tgt), model.EncodePieces(u.tgt), u.context, u.state, u.mask, w.now, store.SourceHash(model.Plain(u.src))); err != nil {
				return err
			}
			stats.Inserted++
			continue
		}
		e.seen = true
		src := model.Plain(u.src)
		human := e.state >= model.Translated

		// Start from what is stored, then apply the incoming data.
		tgt, tgtBlob, state, changed := nullToAny(e.target), e.targetPieces, e.state, e.changed
		var tgtPieces []model.Piece
		sourceChanged := src != e.source
		switch {
		case sourceChanged && human:
			state, changed = model.Translated, true
			stats.SourceChanged++
		case sourceChanged:
			tgt, tgtBlob, state, changed = plainOrNil(u.tgt), model.EncodePieces(u.tgt), u.state, true
			tgtPieces = u.tgt
			stats.SourceChanged++
		case !human && u.tgt != nil:
			tgt, tgtBlob, state = model.Plain(u.tgt), model.EncodePieces(u.tgt), u.state
			tgtPieces = u.tgt
		}
		context := u.context
		if context == "" {
			context = e.context
		}

		// QA against the stored target when we kept it.
		if tgtPieces == nil && tgt != nil {
			if tgtPieces, err = model.DecodePieces(tgtBlob, tgt.(string)); err != nil {
				return err
			}
		}
		mask := w.qaMask(u, tgtPieces, tgt != nil, state)

		if !sourceChanged && !e.obsolete && e.ord == int64(i) && context == e.context &&
			anyEq(tgt, e.target) && state == e.state && changed == e.changed && mask == e.mask {
			continue
		}
		if _, err := w.upd.ExecContext(w.ctx, i, src, model.EncodePieces(u.src), tgt, tgtBlob, context,
			state, mask, changed, w.now, store.SourceHash(src), e.id); err != nil {
			return err
		}
		stats.Updated++
	}
	for _, e := range old {
		if !e.seen && !e.obsolete {
			if _, err := w.tx.ExecContext(w.ctx, "UPDATE units SET obsolete = 1 WHERE id = ?", e.id); err != nil {
				return err
			}
			stats.Obsolete++
		}
	}
	return nil
}

// qaMask reuses the mask computed by the parser when the stored target is
// exactly the incoming one; a kept translation is rechecked against the
// (possibly new) source.
func (w *writer) qaMask(u unit, tgt []model.Piece, has bool, state model.State) qa.Check {
	if !has {
		return 0
	}
	if u.tgt != nil && state == u.state && model.Plain(u.tgt) == model.Plain(tgt) {
		return u.mask
	}
	return w.runner.Mask(qa.Unit{Source: u.src, Target: tgt, State: state})
}

func nullToAny(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func anyEq(a any, b sql.NullString) bool {
	if a == nil {
		return !b.Valid
	}
	return b.Valid && a.(string) == b.String
}
