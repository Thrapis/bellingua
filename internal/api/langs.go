package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/Thrapis/bellingua/internal/importer"
)

// listLangs returns the project's extra (reference) languages.
func (s *Server) listLangs(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	langs, err := s.st.ProjectLangs(r.Context(), p.ID)
	if err != nil {
		return err
	}
	return ok(w, langs)
}

// importLangJob attaches an extra language from an XLIFF tree laid out like
// the project's own import.
func (s *Server) importLangJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var body struct {
		Dir  string `json:"dir"`
		Lang string `json:"lang"` // "" = from the files
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if fi, err := os.Stat(body.Dir); err != nil || !fi.IsDir() {
		return badRequest("not a directory: %s", body.Dir)
	}
	// Same job kind as the main import: the two must not run at once.
	return s.startOnce(w, "import", p, func(ctx context.Context, pr Progress) (any, error) {
		return importer.ImportLang(ctx, s.st, importer.LangOptions{
			ProjectID: p.ID, Root: body.Dir, Lang: strings.TrimSpace(body.Lang), Log: s.log,
			Progress: func(done, total int) { pr.Set(int64(done), int64(total)) },
		})
	})
}

func (s *Server) deleteLang(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	if err := s.st.DeleteProjectLang(r.Context(), p.ID, r.PathValue("lang")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// unitLangs returns a unit's texts in the extra languages. Separate from
// GET /api/units/{id}, so the UI loads them only while their section is open.
func (s *Server) unitLangs(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	langs, err := s.st.UnitLangs(r.Context(), id)
	if err != nil {
		return err
	}
	return ok(w, langs)
}
