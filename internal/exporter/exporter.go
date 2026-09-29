// Package exporter writes a project back out as a tree of XLIFF 1.2 or
// Crowdin CSV files, mirroring the imported layout. Files are exported in
// parallel, each worker streaming its units from its own read connection.
package exporter

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Thrapis/bellingua/internal/formats/crowdincsv"
	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// Options configures an export.
type Options struct {
	ProjectID int64
	Out       string
	Format    string      // xliff | crowdin-csv | "" = each file in its imported format
	MinState  model.State // translations below this state are written as untranslated
	Workers   int
	Progress  func(done, total int)
}

// Stats summarises an export.
type Stats struct {
	Files      int
	Units      int
	Translated int // units written with a target
	Dropped    int // targets the XLIFF writer refused (code mismatch)
	Duration   time.Duration
}

// Export runs an export.
func Export(ctx context.Context, st *store.Store, opt Options) (Stats, error) {
	start := time.Now()
	p, err := st.Project(ctx, opt.ProjectID)
	if err != nil {
		return Stats{}, err
	}
	files, err := st.Files(ctx, opt.ProjectID)
	if err != nil {
		return Stats{}, err
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	var units, translated, dropped, done atomic.Int64
	jobs := make(chan *store.File)
	errc := make(chan error, workers)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				n, t, d, err := exportFile(ctx, st, p, f, opt)
				if err != nil {
					errc <- fmt.Errorf("%s: %w", f.Path, err)
					cancel()
					return
				}
				units.Add(int64(n))
				translated.Add(int64(t))
				dropped.Add(int64(d))
				if n := done.Add(1); opt.Progress != nil {
					opt.Progress(int(n), len(files))
				}
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errc)
	if err := <-errc; err != nil {
		return Stats{}, err
	}
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	return Stats{
		Files: len(files), Units: int(units.Load()), Translated: int(translated.Load()),
		Dropped: int(dropped.Load()), Duration: time.Since(start),
	}, nil
}

type row struct {
	key, context string
	src, tgt     []model.Piece
	state        model.State
}

func exportFile(ctx context.Context, st *store.Store, p *store.Project, f *store.File, opt Options) (n, translated, dropped int, err error) {
	rows, err := st.R.QueryContext(ctx, `SELECT key, context, source, source_pieces, target, target_pieces, state
		FROM units WHERE file_id = ? AND obsolete = 0 ORDER BY ord`, f.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	var units []row
	for rows.Next() {
		var r row
		var src string
		var tgt sql.NullString
		var sb, tb []byte
		if err := rows.Scan(&r.key, &r.context, &src, &sb, &tgt, &tb, &r.state); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		if r.src, err = model.DecodePieces(sb, src); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		if tgt.Valid && r.state >= opt.MinState {
			if r.tgt, err = model.DecodePieces(tb, tgt.String); err != nil {
				rows.Close()
				return 0, 0, 0, err
			}
			translated++
		}
		units = append(units, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	format := opt.Format
	if format == "" {
		format = f.Format
	}
	rel := f.Path
	ext := ".xlf"
	if format == "crowdin-csv" {
		ext = ".csv"
	}
	if !strings.EqualFold(path.Ext(rel), ext) && !(ext == ".xlf" && strings.EqualFold(path.Ext(rel), ".xliff")) {
		rel = strings.TrimSuffix(rel, path.Ext(rel)) + ext
	}
	out := filepath.Join(opt.Out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return 0, 0, 0, err
	}
	fh, err := os.Create(out)
	if err != nil {
		return 0, 0, 0, err
	}
	bw := bufio.NewWriterSize(fh, 256<<10)
	switch format {
	case "crowdin-csv":
		cr := make([]crowdincsv.Row, len(units))
		for i, u := range units {
			cr[i] = crowdincsv.Row{ID: u.key, Source: model.Plain(u.src), Context: u.context}
			if u.tgt != nil {
				cr[i].Translation = model.Plain(u.tgt)
			}
		}
		err = crowdincsv.Write(bw, cr)
	default:
		original := f.Original
		if original == "" {
			original = f.Path
		}
		xf := &xliff.File{Original: original, SourceLang: p.SourceLang, TargetLang: p.TargetLang, Units: make([]xliff.Unit, len(units))}
		for i, u := range units {
			xf.Units[i] = xliff.Unit{ID: u.key, Note: u.context, Source: u.src, Target: u.tgt}
			if u.tgt != nil {
				xf.Units[i].State = u.state.XLIFFState()
			}
		}
		var lost []string
		lost, err = xliff.Write(bw, xf)
		dropped = len(lost)
	}
	if err == nil {
		err = bw.Flush()
	}
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	return len(units), translated - dropped, dropped, err
}
