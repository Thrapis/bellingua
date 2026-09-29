// Package api is the HTTP layer: a JSON API under /api and the embedded web
// UI everywhere else.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/gzhttp"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/glossary"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/mt"
	"github.com/Thrapis/bellingua/internal/mtserver"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
	"github.com/Thrapis/bellingua/internal/tm"
)

// Server holds the dependencies of the handlers.
type Server struct {
	st     *store.Store
	cfg    config.Config
	mt     mt.Chain
	backup *backup.Manager
	jobs   *Jobs
	log    *slog.Logger
	static fs.FS // the exported Next.js app; nil disables the UI

	mtStatus func() mtserver.Status // nil when no local MT server is supervised

	runnersMu sync.Mutex
	runners   map[int64]runnerEntry

	tm *tm.Manager // fuzzy translation-memory indexes

	qaTimersMu sync.Mutex
	qaTimers   map[int64]*time.Timer // debounced QA rechecks after glossary edits

	treeMu sync.Mutex
	trees  map[int64]*treeCache
}

type runnerEntry struct {
	cfg      string
	glossRev int64
	gloss    *glossary.Glossary
	runner   *qa.Runner
}

// New builds a server. base bounds the lifetime of background jobs.
func New(base context.Context, st *store.Store, cfg config.Config, chain mt.Chain, bm *backup.Manager, static fs.FS, log *slog.Logger) *Server {
	return &Server{
		st: st, cfg: cfg, mt: chain, backup: bm, jobs: NewJobs(base), log: log, static: static,
		runners: map[int64]runnerEntry{}, trees: map[int64]*treeCache{}, qaTimers: map[int64]*time.Timer{},
		tm: tm.NewManager(st.HumanUnits),
	}
}

// SetMTStatus lets the API report (and wait on) the local MT server.
func (s *Server) SetMTStatus(fn func() mtserver.Status) { s.mtStatus = fn }

// mtUnavailable explains why machine translation cannot run right now, or
// returns nil when it can (or when nothing is supervised).
func (s *Server) mtUnavailable() error {
	if s.mtStatus == nil {
		return nil
	}
	switch st := s.mtStatus(); st.State {
	case mtserver.Starting:
		return &httpError{code: http.StatusServiceUnavailable, msg: "The machine translation server is starting (loading the model) — try again in a few seconds."}
	case mtserver.Failed, mtserver.Off:
		return &httpError{code: http.StatusServiceUnavailable, msg: "The machine translation server is not running: " + st.Message}
	}
	return nil
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := fn(w, r); err != nil {
				s.fail(w, r, err)
			}
		})
	}
	h("GET /api/info", s.info)
	h("GET /api/projects", s.listProjects)
	h("POST /api/projects", s.createProject)
	h("GET /api/projects/{pid}", s.getProject)
	h("DELETE /api/projects/{pid}", s.deleteProject)
	h("PUT /api/projects/{pid}/settings", s.saveSettings)
	h("GET /api/projects/{pid}/tree", s.tree)
	h("GET /api/projects/{pid}/units", s.listUnits)
	h("GET /api/projects/{pid}/count", s.countUnits)
	h("POST /api/projects/{pid}/bulk", s.bulk)
	h("POST /api/projects/{pid}/qa", s.qaJob)
	h("POST /api/projects/{pid}/mt", s.mtJob)
	h("POST /api/projects/{pid}/import", s.importJob)
	h("GET /api/projects/{pid}/langs", s.listLangs)
	h("POST /api/projects/{pid}/langs/import", s.importLangJob)
	h("DELETE /api/projects/{pid}/langs/{lang}", s.deleteLang)
	h("POST /api/projects/{pid}/export", s.exportJob)
	h("GET /api/projects/{pid}/terms", s.listTerms)
	h("POST /api/projects/{pid}/terms", s.createTerm)
	h("GET /api/projects/{pid}/terms/export", s.exportTerms)
	h("GET /api/projects/{pid}/terms/forms", s.termForms)
	h("POST /api/projects/{pid}/terms/import", s.importTerms)
	h("PUT /api/terms/{id}", s.updateTerm)
	h("DELETE /api/terms/{id}", s.deleteTerm)
	h("GET /api/units/{id}/tm", s.unitTM)
	h("POST /api/units/{id}/propagate", s.propagate)
	h("POST /api/projects/{pid}/tm/fill", s.tmFillJob)
	h("GET /api/units/{id}", s.getUnit)
	h("PUT /api/units/{id}", s.saveUnit)
	h("POST /api/units/{id}/mt", s.unitMT)
	h("GET /api/units/{id}/langs", s.unitLangs)
	h("GET /api/jobs", s.listJobs)
	h("GET /api/jobs/events", s.jobEvents)
	h("DELETE /api/jobs/{id}", s.cancelJob)
	h("GET /api/backup", s.backupStatus)
	h("POST /api/backup", s.backupNow)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint", nil)
	})
	if s.static != nil {
		mux.Handle("/", spa(s.static))
	}

	gz, _ := gzhttp.NewWrapper(gzhttp.MinSize(1024), gzhttp.CompressionLevel(1),
		gzhttp.ExceptContentTypes([]string{"text/event-stream"}))
	return gz(mux)
}

// --- helpers -----------------------------------------------------------------

type httpError struct {
	code int
	msg  string
	data any
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &httpError{code: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	var mm *model.CodeMismatch
	switch {
	case errors.As(err, &he):
		writeError(w, he.code, he.msg, he.data)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found", nil)
	case errors.As(err, &mm):
		writeError(w, http.StatusUnprocessableEntity, mm.Error(), map[string]any{"missing": mm.Missing, "extra": mm.Extra})
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error(), nil)
	case errors.Is(err, store.ErrSkipped):
		writeError(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, context.Canceled):
		// client went away
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, err.Error(), nil)
	}
}

func writeError(w http.ResponseWriter, code int, msg string, data any) {
	writeJSON(w, code, map[string]any{"error": msg, "data": data})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func ok(w http.ResponseWriter, v any) error { writeJSON(w, http.StatusOK, v); return nil }

// etag answers 304 when the client already has revision rev of a resource.
func etag(w http.ResponseWriter, r *http.Request, rev int64) bool {
	tag := `W/"` + strconv.FormatInt(rev, 36) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20))
	if err := dec.Decode(v); err != nil {
		return badRequest("bad JSON body: %v", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, badRequest("bad %s", name)
	}
	return id, nil
}

func (s *Server) project(r *http.Request) (*store.Project, error) {
	id, err := pathID(r, "pid")
	if err != nil {
		return nil, err
	}
	return s.st.Project(r.Context(), id)
}

// runner returns the (cached) QA runner and glossary for a project's current
// settings and glossary revision.
func (s *Server) runner(ctx context.Context, p *store.Project) (*qa.Runner, *glossary.Glossary, error) {
	key, _ := json.Marshal(p.Settings.QA)
	s.runnersMu.Lock()
	e, ok := s.runners[p.ID]
	s.runnersMu.Unlock()
	if ok && e.cfg == string(key) && e.glossRev == p.GlossRev {
		return e.runner, e.gloss, nil
	}
	g, err := s.st.Glossary(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	e = runnerEntry{cfg: string(key), glossRev: p.GlossRev, gloss: g, runner: qa.NewRunner(p.Settings.QA).WithGlossary(g)}
	s.runnersMu.Lock()
	s.runners[p.ID] = e
	s.runnersMu.Unlock()
	return e.runner, e.gloss, nil
}

// filter parses the unit filter from the query string.
func filter(p *store.Project, r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	f := store.Filter{ProjectID: p.ID, Dir: q.Get("dir"), Query: q.Get("q"), In: q.Get("in"),
		Changed: q.Get("changed") == "1", Errors: q.Get("errors") == "1"}
	if v := q.Get("same"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, badRequest("bad same")
		}
		f.SameAs = id
	}
	if v := q.Get("file"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, badRequest("bad file")
		}
		f.FileID = id
	}
	for _, name := range splitList(q.Get("state")) {
		st, ok := model.ParseState(name)
		if !ok {
			return f, badRequest("unknown state %q", name)
		}
		f.States = append(f.States, st)
	}
	for _, id := range splitList(q.Get("qa")) {
		if id == "any" {
			f.QA = qa.Check(^uint32(0))
			break
		}
		c, ok := qa.ByID(id)
		if !ok {
			return f, badRequest("unknown QA check %q", id)
		}
		f.QA |= c
	}
	return f, nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// --- handlers ----------------------------------------------------------------

func (s *Server) info(w http.ResponseWriter, r *http.Request) error {
	var providers []string
	for _, p := range s.mt {
		providers = append(providers, p.Name())
	}
	states := []string{}
	for st := model.Untranslated; st <= model.Approved; st++ {
		states = append(states, st.String())
	}
	return ok(w, map[string]any{
		"mtProviders": providers, "mtServer": s.mtServerStatus(), "qaChecks": qa.All, "states": states,
		"previewRunes": store.PreviewRunes, "backup": s.backup.Status(),
	})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) error {
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	ps, err := s.st.Projects(r.Context())
	if err != nil {
		return err
	}
	return ok(w, ps)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name       string `json:"name"`
		SourceLang string `json:"sourceLang"`
		TargetLang string `json:"targetLang"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || body.SourceLang == "" || body.TargetLang == "" {
		return badRequest("name, sourceLang and targetLang are required")
	}
	if _, err := s.st.ProjectByName(r.Context(), body.Name); err == nil {
		return &httpError{code: http.StatusConflict, msg: "a project with this name exists"}
	}
	p, err := s.st.CreateProject(r.Context(), body.Name, body.SourceLang, body.TargetLang)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) error {
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	p, err := s.project(r)
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	if err := s.st.DeleteProject(r.Context(), p.ID); err != nil {
		return err
	}
	s.tm.Invalidate(p.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var st store.Settings
	if err := decode(r, &st); err != nil {
		return err
	}
	if err := s.st.SaveSettings(r.Context(), p.ID, st); err != nil {
		return err
	}
	p.Settings = st
	return ok(w, p)
}

type unitsPage struct {
	Rows []store.Row   `json:"rows"`
	Next *store.Cursor `json:"next"`
}

func (s *Server) listUnits(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	f, err := filter(p, r)
	if err != nil {
		return err
	}
	limit := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	sort, okSort := store.ParseSort(r.URL.Query().Get("sort"))
	if !okSort {
		return badRequest("unknown sort %q", r.URL.Query().Get("sort"))
	}
	var after *store.Cursor
	if v := r.URL.Query().Get("after"); v != "" {
		a, b, found := strings.Cut(v, ".")
		x, err1 := strconv.ParseInt(a, 10, 64)
		y, err2 := strconv.ParseInt(b, 10, 64)
		if !found || err1 != nil || err2 != nil {
			return badRequest("bad cursor")
		}
		after = &store.Cursor{A: x, B: y}
	}
	rows, next, err := s.st.List(r.Context(), f, sort, after, limit)
	if err != nil {
		return err
	}
	return ok(w, unitsPage{Rows: rows, Next: next})
}

func (s *Server) countUnits(w http.ResponseWriter, r *http.Request) error {
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	p, err := s.project(r)
	if err != nil {
		return err
	}
	f, err := filter(p, r)
	if err != nil {
		return err
	}
	n, err := s.st.Count(r.Context(), f)
	if err != nil {
		return err
	}
	return ok(w, map[string]int64{"count": n})
}

type unitResponse struct {
	*store.Unit
	Issues     []qa.Issue  `json:"issues"`
	Terms      []termMatch `json:"terms"`
	Duplicates int         `json:"duplicates,omitempty"` // set by save only
}

// termMatch is a glossary term found in the unit's source, with where.
type termMatch struct {
	glossary.Term
	Spans   [][2]int `json:"spans"`   // UTF-16 offsets into the plain source
	Inserts []string `json:"inserts"` // per span: the translation of that form
}

func (s *Server) unitWithIssues(ctx context.Context, u *store.Unit) (unitResponse, error) {
	p, err := s.st.Project(ctx, u.ProjectID)
	if err != nil {
		return unitResponse{}, err
	}
	runner, g, err := s.runner(ctx, p)
	if err != nil {
		return unitResponse{}, err
	}
	_, issues := runner.Run(qa.Unit{Source: u.Source, Target: u.Target, State: u.State})
	if issues == nil {
		issues = []qa.Issue{}
	}
	terms := []termMatch{}
	idx := map[int64]int{}
	for _, m := range g.Match(u.Source) {
		i, ok := idx[m.TermID]
		if !ok {
			t, _ := g.Term(m.TermID)
			i = len(terms)
			idx[m.TermID] = i
			terms = append(terms, termMatch{Term: t})
		}
		terms[i].Spans = append(terms[i].Spans, [2]int{m.Start, m.End})
		terms[i].Inserts = append(terms[i].Inserts, m.Insert)
	}
	return unitResponse{Unit: u, Issues: issues, Terms: terms}, nil
}

func (s *Server) getUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.st.Unit(r.Context(), id, r.URL.Query().Get("history") != "0")
	if err != nil {
		return err
	}
	resp, err := s.unitWithIssues(r.Context(), u)
	if err != nil {
		return err
	}
	return ok(w, resp)
}

func (s *Server) saveUnit(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		Target *string     `json:"target"`
		State  model.State `json:"state"`
		Force  bool        `json:"force"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	cur, err := s.st.Unit(r.Context(), id, false)
	if err != nil {
		return err
	}
	p, err := s.st.Project(r.Context(), cur.ProjectID)
	if err != nil {
		return err
	}
	runner, _, err := s.runner(r.Context(), p)
	if err != nil {
		return err
	}
	u, err := s.st.SaveUnit(r.Context(), id, store.Save{Target: body.Target, State: body.State, Origin: "edit", Force: body.Force}, runner)
	if err != nil {
		return err
	}
	human := u.Target != nil && u.State >= model.Translated
	s.tm.Update(u.ProjectID, u.ID, u.Source, human)
	resp, err := s.unitWithIssues(r.Context(), u)
	if err != nil {
		return err
	}
	// Identical strings still waiting for a human translation: the UI offers
	// to apply this one to them.
	if human {
		dups, err := s.st.PendingDuplicates(r.Context(), u.ProjectID, u.ID, model.Plain(u.Source))
		if err != nil {
			return err
		}
		resp.Duplicates = len(dups)
	}
	return ok(w, resp)
}

func (s *Server) unitMT(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	u, err := s.st.Unit(r.Context(), id, false)
	if err != nil {
		return err
	}
	p, err := s.st.Project(r.Context(), u.ProjectID)
	if err != nil {
		return err
	}
	if err := s.mtUnavailable(); err != nil {
		return err
	}
	// Alongside the plain translation, one with the glossary terms pinned
	// (masked, then inserted in their agreed form). Both run at once.
	type pinnedResult struct {
		tgt []model.Piece
		err error
	}
	var pinnedCh chan pinnedResult
	if _, g, err := s.runner(r.Context(), p); err == nil {
		if src, repl := g.Pin(u.Source); len(repl) > 0 {
			pinnedCh = make(chan pinnedResult, 1)
			go func() {
				res, err := s.mt.TranslatePieces(r.Context(), src, p.SourceLang, p.TargetLang)
				pinnedCh <- pinnedResult{glossary.Unpin(res.Target, repl), err}
			}()
		}
	}
	res, err := s.mt.TranslatePieces(r.Context(), u.Source, p.SourceLang, p.TargetLang)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &httpError{code: http.StatusBadGateway, msg: err.Error()}
	}
	out := struct {
		mt.Result
		Glossary []model.Piece `json:"glossary,omitempty"`
	}{Result: res}
	if pinnedCh != nil {
		if pr := <-pinnedCh; pr.err == nil {
			out.Glossary = pr.tgt
		} else if !errors.Is(pr.err, context.Canceled) {
			s.log.Warn("mt with glossary", "unit", id, "err", pr.err)
		}
	}
	return ok(w, out)
}

func (s *Server) bulk(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	f, err := filter(p, r)
	if err != nil {
		return err
	}
	var body struct {
		State    model.State `json:"state"`
		NoErrors bool        `json:"noErrors"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.State == model.Untranslated {
		return badRequest("bulk state must be mt, translated or approved")
	}
	n, err := s.st.SetState(r.Context(), f, body.State, body.NoErrors)
	if err != nil {
		return err
	}
	s.tm.Invalidate(p.ID)
	return ok(w, map[string]int64{"changed": n})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) error { return ok(w, s.jobs.List()) }

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) error {
	if !s.jobs.Cancel(r.PathValue("id")) {
		return store.ErrNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// jobEvents streams the job list over SSE whenever anything changes.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) error {
	fl, okf := w.(http.Flusher)
	if !okf {
		return errors.New("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func() {
		b, _ := json.Marshal(s.jobs.List())
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	send()
	last := s.jobs.ver.Load()
	tick := time.NewTicker(250 * time.Millisecond)
	keep := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-tick.C:
			if v := s.jobs.ver.Load(); v != last {
				last = v
				send()
			}
		case <-keep.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) backupStatus(w http.ResponseWriter, r *http.Request) error {
	return ok(w, s.backup.Status())
}

func (s *Server) backupNow(w http.ResponseWriter, r *http.Request) error {
	if !s.backup.Status().Configured {
		return badRequest("no backup target configured (backup.dir / backup.sftp in bellingua.yaml)")
	}
	job := s.jobs.Start("backup", 0, func(ctx context.Context, p Progress) (any, error) {
		p.Say("snapshotting and uploading")
		res, err := s.backup.Now(ctx)
		return res, err
	})
	writeJSON(w, http.StatusAccepted, job)
	return nil
}

func (s *Server) mtServerStatus() *mtserver.Status {
	if s.mtStatus == nil {
		return nil
	}
	st := s.mtStatus()
	return &st
}
