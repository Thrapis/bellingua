package importer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// LangOptions configures an extra-language import.
type LangOptions struct {
	ProjectID int64
	Root      string // XLIFF tree laid out like the project's own import
	Lang      string // "" = the files' target-language
	Workers   int
	Progress  func(done, total int)
	Log       *slog.Logger
}

// LangStats summarises an extra-language import.
type LangStats struct {
	Lang         string        `json:"lang"`
	Files        int           `json:"files"`
	Stored       int           `json:"stored"`       // texts attached to units
	Outdated     int           `json:"outdated"`     // made from a different source
	NoTarget     int           `json:"noTarget"`     // units without a translation
	UnknownFiles int           `json:"unknownFiles"` // files the project does not have
	UnknownUnits int           `json:"unknownUnits"` // keys the project's file does not have
	Problems     int           `json:"problems"`     // targets rejected by the XLIFF reader
	Duration     time.Duration `json:"duration"`
}

// ImportLang attaches the texts of an extra language (English, Ukrainian...)
// to the project's units, matched by (file path, unit key). It never creates
// units or files, and it replaces everything the language had. The format is
// the same XLIFF tree as the main import: <source> is the project's source
// text (a different one marks the text outdated), <target> the language's.
func ImportLang(ctx context.Context, st *store.Store, opt LangOptions) (LangStats, error) {
	start := time.Now()
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p, err := st.Project(ctx, opt.ProjectID)
	if err != nil {
		return LangStats{}, err
	}
	paths, err := listFiles(opt.Root, XLIFF)
	if err != nil {
		return LangStats{}, err
	}
	if len(paths) == 0 {
		return LangStats{}, fmt.Errorf("import: no XLIFF files under %s", opt.Root)
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	type parsedLang struct {
		path     string
		file     *xliff.File
		problems int
		err      error
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	results := make(chan parsedLang, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				r := parsedLang{path: rel}
				if fh, err := os.Open(filepath.Join(opt.Root, filepath.FromSlash(rel))); err != nil {
					r.err = err
				} else {
					var problems []xliff.Problem
					r.file, problems, r.err = xliff.Read(fh)
					r.problems = len(problems)
					fh.Close()
					if r.err != nil {
						r.err = fmt.Errorf("%s: %w", rel, r.err)
					}
				}
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
		for _, rel := range paths {
			select {
			case jobs <- rel:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	stats := LangStats{Lang: strings.TrimSpace(opt.Lang)}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		var w *store.LangWriter
		defer func() {
			if w != nil {
				w.Close()
			}
		}()
		done := 0
		for r := range results {
			if r.err != nil {
				return r.err
			}
			// The language comes from the option or from the files, and all
			// files must agree: a mixed tree is almost certainly a mistake.
			if fl := r.file.TargetLang; fl != "" {
				if stats.Lang == "" {
					stats.Lang = fl
				} else if !sameLang(fl, stats.Lang) {
					return fmt.Errorf("%s: target-language %q, expected %q", r.path, fl, stats.Lang)
				}
			}
			if w == nil {
				if stats.Lang == "" {
					return fmt.Errorf("%s: no target-language in the file; choose the language", r.path)
				}
				if sameLang(stats.Lang, p.SourceLang) || sameLang(stats.Lang, p.TargetLang) {
					return fmt.Errorf("%q: %w", stats.Lang, store.ErrMainLang)
				}
				var err error
				if w, err = store.NewLangWriter(ctx, tx, p, stats.Lang); err != nil {
					return err
				}
			}
			if err := writeLangFile(w, r.file, r.path, &stats); err != nil {
				return fmt.Errorf("%s: %w", r.path, err)
			}
			stats.Files++
			stats.Problems += r.problems
			done++
			if opt.Progress != nil {
				opt.Progress(done, len(paths))
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return w.Finish(time.Now().Unix())
	})
	stats.Duration = time.Since(start)
	if err == nil {
		log.Info("language import done", "lang", stats.Lang, "files", stats.Files, "stored", stats.Stored,
			"outdated", stats.Outdated, "unknownFiles", stats.UnknownFiles, "unknownUnits", stats.UnknownUnits,
			"took", stats.Duration.Round(time.Millisecond))
	}
	return stats, err
}

func writeLangFile(w *store.LangWriter, f *xliff.File, path string, stats *LangStats) error {
	units, err := w.Units(path)
	if err != nil {
		return err
	}
	if units == nil {
		stats.UnknownFiles++
		return nil
	}
	for _, u := range f.Units {
		if u.Target == nil || model.Plain(u.Target) == "" {
			stats.NoTarget++
			continue
		}
		ref, ok := units[u.ID]
		if !ok {
			stats.UnknownUnits++
			continue
		}
		state := model.Approved // official texts usually carry no state
		if u.State != "" {
			state = model.FromXLIFFState(u.State)
		}
		h := store.SourceHash(model.Plain(u.Source))
		if h != ref.SourceHash {
			stats.Outdated++
		}
		if err := w.Put(ref.ID, u.Target, state, h); err != nil {
			return err
		}
		stats.Stored++
	}
	return nil
}

// sameLang compares language codes loosely: "uk" matches "uk-UA".
func sameLang(a, b string) bool {
	base := func(s string) string {
		s = strings.ToLower(s)
		if i := strings.IndexAny(s, "-_"); i > 0 {
			s = s[:i]
		}
		return s
	}
	return base(a) == base(b)
}
