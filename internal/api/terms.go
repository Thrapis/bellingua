package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Thrapis/bellingua/internal/glossary"
)

// termBody is the JSON a client sends for a term.
type termBody struct {
	Source        string          `json:"source"`
	Target        string          `json:"target"`
	Note          string          `json:"note"`
	DNT           bool            `json:"dnt"`
	CaseSensitive bool            `json:"caseSensitive"`
	Forms         []glossary.Form `json:"forms"`
}

func (b termBody) term() glossary.Term {
	return glossary.Term{Source: b.Source, Target: b.Target, Note: b.Note, DNT: b.DNT, CaseSensitive: b.CaseSensitive, Forms: b.Forms}
}

// termForms lists the forms of ?source found in the project's texts.
func (s *Server) termForms(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	src := strings.TrimSpace(r.URL.Query().Get("source"))
	if src == "" {
		return badRequest("source is required")
	}
	forms, err := s.st.TermForms(r.Context(), p, src)
	if err != nil {
		return err
	}
	return ok(w, forms)
}

func (s *Server) listTerms(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	// The store revision, not gloss_rev: a recreated project may reuse the
	// id and start again at gloss_rev 0.
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	terms, err := s.st.Terms(r.Context(), p.ID)
	if err != nil {
		return err
	}
	return ok(w, terms)
}

func (s *Server) createTerm(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var b termBody
	if err := decode(r, &b); err != nil {
		return err
	}
	t, err := s.st.CreateTerm(r.Context(), p.ID, b.term())
	if err != nil {
		return err
	}
	s.glossaryChanged(p.ID)
	writeJSON(w, http.StatusCreated, t)
	return nil
}

func (s *Server) updateTerm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var b termBody
	if err := decode(r, &b); err != nil {
		return err
	}
	t, err := s.st.UpdateTerm(r.Context(), id, b.term())
	if err != nil {
		return err
	}
	if pid, err := s.st.TermProject(r.Context(), id); err == nil {
		s.glossaryChanged(pid)
	}
	return ok(w, t)
}

func (s *Server) deleteTerm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pid, err := s.st.TermProject(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.st.DeleteTerm(r.Context(), id); err != nil {
		return err
	}
	s.glossaryChanged(pid)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// csvHeader is the glossary exchange format; import also accepts any column
// order and extra columns, matched by header name.
var csvHeader = []string{"source", "target", "note", "dnt", "case_sensitive", "forms"}

// formsCSV writes forms as "Баумана=Баўмана; Бауману=Баўману".
func formsCSV(fs []glossary.Form) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Src + "=" + f.Tgt
	}
	return strings.Join(parts, "; ")
}

// parseFormsCSV reads formsCSV's format; malformed pairs are skipped.
func parseFormsCSV(s string) []glossary.Form {
	var out []glossary.Form
	for _, part := range strings.Split(s, ";") {
		src, tgt, ok := strings.Cut(part, "=")
		if src, tgt = strings.TrimSpace(src), strings.TrimSpace(tgt); ok && src != "" && tgt != "" {
			out = append(out, glossary.Form{Src: src, Tgt: tgt})
		}
	}
	return out
}

func (s *Server) exportTerms(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	terms, err := s.st.Terms(r.Context(), p.ID)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-glossary.csv"`, p.Name))
	cw := csv.NewWriter(w)
	cw.Write(csvHeader)
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return ""
	}
	for _, t := range terms {
		cw.Write([]string{t.Source, t.Target, t.Note, flag(t.DNT), flag(t.CaseSensitive), formsCSV(t.Forms)})
	}
	cw.Flush()
	return cw.Error()
}

func (s *Server) importTerms(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	terms, err := readTermsCSV(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		return badRequest("%v", err)
	}
	ins, upd, err := s.st.UpsertTerms(r.Context(), p.ID, terms)
	if err != nil {
		return err
	}
	s.glossaryChanged(p.ID)
	return ok(w, map[string]int{"inserted": ins, "updated": upd})
}

func readTermsCSV(rd io.Reader) ([]glossary.Term, error) {
	cr := csv.NewReader(rd)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("glossary csv: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\xEF\xBB\xBF")))
		col[h] = i
	}
	if _, ok := col["source"]; !ok {
		return nil, fmt.Errorf("glossary csv: header must have a %q column (have %v)", "source", head)
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	truthy := func(v string) bool {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "x":
			return true
		}
		return false
	}
	var out []glossary.Term
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("glossary csv: %w", err)
		}
		out = append(out, glossary.Term{
			Source: get(rec, "source"), Target: get(rec, "target"), Note: get(rec, "note"),
			DNT: truthy(get(rec, "dnt")), CaseSensitive: truthy(get(rec, "case_sensitive")),
			Forms: parseFormsCSV(get(rec, "forms")),
		})
	}
}

// glossaryChanged schedules a QA recheck of the project. Edits usually come
// in bursts (a CSV import, several quick fixes), so the recheck is debounced
// and waits for a running QA job to finish.
func (s *Server) glossaryChanged(projectID int64) {
	s.qaTimersMu.Lock()
	defer s.qaTimersMu.Unlock()
	if t, ok := s.qaTimers[projectID]; ok {
		t.Stop()
	}
	var fire func()
	fire = func() {
		if s.jobs.Running("qa", projectID) {
			s.qaTimersMu.Lock()
			s.qaTimers[projectID] = time.AfterFunc(2*time.Second, fire)
			s.qaTimersMu.Unlock()
			return
		}
		ctx := s.jobs.base
		p, err := s.st.Project(ctx, projectID)
		if err != nil {
			return
		}
		runner, _, err := s.runner(ctx, p)
		if err != nil {
			s.log.Warn("glossary recheck", "err", err)
			return
		}
		s.jobs.Start("qa", projectID, func(ctx context.Context, pr Progress) (any, error) {
			pr.Say("glossary changed: rechecking")
			n, err := s.st.RecheckQA(ctx, projectID, runner, func(done, total int64) { pr.Set(done, total) })
			return map[string]int64{"changed": n}, err
		})
	}
	s.qaTimers[projectID] = time.AfterFunc(2*time.Second, fire)
}
