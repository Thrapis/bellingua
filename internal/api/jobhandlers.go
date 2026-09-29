package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/Thrapis/bellingua/internal/exporter"
	"github.com/Thrapis/bellingua/internal/glossary"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

func (s *Server) startOnce(w http.ResponseWriter, kind string, p *store.Project, fn func(ctx context.Context, pr Progress) (any, error)) error {
	if s.jobs.Running(kind, p.ID) {
		return &httpError{code: http.StatusConflict, msg: kind + " already running for this project"}
	}
	writeJSON(w, http.StatusAccepted, s.jobs.Start(kind, p.ID, fn))
	return nil
}

func (s *Server) qaJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	runner, _, err := s.runner(r.Context(), p)
	if err != nil {
		return err
	}
	return s.startOnce(w, "qa", p, func(ctx context.Context, pr Progress) (any, error) {
		n, err := s.st.RecheckQA(ctx, p.ID, runner, func(done, total int64) { pr.Set(done, total) })
		return map[string]int64{"changed": n}, err
	})
}

func (s *Server) importJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var body struct {
		Dir    string `json:"dir"`
		Format string `json:"format"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if fi, err := os.Stat(body.Dir); err != nil || !fi.IsDir() {
		return badRequest("not a directory: %s", body.Dir)
	}
	runner, _, err := s.runner(r.Context(), p)
	if err != nil {
		return err
	}
	return s.startOnce(w, "import", p, func(ctx context.Context, pr Progress) (any, error) {
		defer s.tm.Invalidate(p.ID) // sources and translations may have changed
		return importer.Import(ctx, s.st, runner, importer.Options{
			ProjectID: p.ID, Root: body.Dir, Format: body.Format, Log: s.log,
			Progress: func(done, total int) { pr.Set(int64(done), int64(total)) },
		})
	})
}

func (s *Server) exportJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var body struct {
		Dir      string      `json:"dir"`
		Format   string      `json:"format"`
		MinState model.State `json:"minState"`
	}
	body.MinState = model.MT
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Dir == "" {
		return badRequest("dir is required")
	}
	if body.MinState == model.Untranslated {
		body.MinState = model.MT
	}
	return s.startOnce(w, "export", p, func(ctx context.Context, pr Progress) (any, error) {
		return exporter.Export(ctx, s.st, exporter.Options{
			ProjectID: p.ID, Out: body.Dir, Format: body.Format, MinState: body.MinState,
			Progress: func(done, total int) { pr.Set(int64(done), int64(total)) },
		})
	})
}

// mtJob machine-translates every unit matching the filter that has no human
// translation. By default only untranslated units are done; overwrite=true
// also re-translates units in state mt. glossary=true pins the glossary terms
// as in unitMT, so they come out in their agreed form.
func (s *Server) mtJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	f, err := filter(p, r)
	if err != nil {
		return err
	}
	var body struct {
		Overwrite bool `json:"overwrite"`
		Glossary  bool `json:"glossary"`
	}
	if r.ContentLength > 0 {
		if err := decode(r, &body); err != nil {
			return err
		}
	}
	f.States = []model.State{model.Untranslated}
	ifBelow := model.MT
	if body.Overwrite {
		f.States = append(f.States, model.MT)
		ifBelow = model.Translated
	}
	if err := s.mtUnavailable(); err != nil {
		return err
	}
	if len(s.mt) == 0 {
		return badRequest("no MT provider configured")
	}
	runner, g, err := s.runner(r.Context(), p)
	if err != nil {
		return err
	}
	if !body.Glossary {
		g = nil
	}
	return s.startOnce(w, "mt", p, func(ctx context.Context, pr Progress) (any, error) {
		total, err := s.st.Count(ctx, f)
		if err != nil {
			return nil, err
		}
		pr.Set(0, total)

		type item struct {
			id  int64
			src []model.Piece
		}
		items := make(chan item)
		var done, saved, failed, skipped atomic.Int64
		var firstErr atomic.Value
		var wg sync.WaitGroup
		for range max(s.cfg.MT.Concurrency, 1) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for it := range items {
					src, repl := it.src, []string(nil)
					if g != nil {
						src, repl = g.Pin(it.src)
					}
					res, err := s.mt.TranslatePieces(ctx, src, p.SourceLang, p.TargetLang)
					if err == nil {
						tgt := res.Target
						if len(repl) > 0 {
							tgt = glossary.Unpin(tgt, repl)
						}
						t := model.Plain(tgt)
						_, err = s.st.SaveUnit(ctx, it.id, store.Save{Target: &t, State: model.MT, Origin: "mt", IfBelow: ifBelow}, runner)
					}
					switch {
					case err == nil:
						saved.Add(1)
					case errors.Is(err, store.ErrSkipped):
						skipped.Add(1)
					case ctx.Err() != nil:
					default:
						failed.Add(1)
						firstErr.CompareAndSwap(nil, err.Error())
					}
					pr.Set(done.Add(1), total)
				}
			}()
		}

		// Page through the filter by cursor. Saved units leave the filter
		// (their state changes), but the keyset cursor moves past them anyway.
		var listErr error
		var cur *store.Cursor
	pages:
		for {
			rows, next, err := s.st.List(ctx, f, store.SortFile, cur, 500)
			if err != nil {
				listErr = err
				break
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				u, err := s.st.Unit(ctx, row.ID, false) // full text, not the preview
				if err != nil {
					listErr = err
					break pages
				}
				select {
				case items <- item{u.ID, u.Source}:
				case <-ctx.Done():
					break pages
				}
			}
			if next == nil {
				break
			}
			cur = next
		}
		close(items)
		wg.Wait()
		res := map[string]any{"translated": saved.Load(), "failed": failed.Load(), "skipped": skipped.Load()}
		if e, _ := firstErr.Load().(string); e != "" {
			res["firstError"] = e
		}
		if listErr != nil {
			return res, listErr
		}
		return res, ctx.Err()
	})
}
