package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// Lookup for the Firefox crowdin-helper extension: given what its Crowdin
// editor shows (project identifier, source text) it answers with translations
// from the linked bellingua project, exact first, then fuzzy.
//
//	GET /api/crowdin/lookup?project=<identifier>&text=<source>[&target=be][&limit=5]
//	GET /api/crowdin/lookup.json?project=<identifier>[&minState=translated]
//
// Both are read-only GETs, answered with CORS headers for extension origins
// only (web pages stay blocked).

const (
	maxLookupText  = 8000
	lookupLimit    = 5
	maxLookupLimit = 20
)

type lookupMatch struct {
	Score  int         `json:"score"` // percent; 100 = identical source
	Source string      `json:"source"`
	Target string      `json:"target"`
	State  model.State `json:"state"`
	Path   string      `json:"path"`
}

type lookupProject struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	SourceLang string `json:"sourceLang"`
	TargetLang string `json:"targetLang"`
}

// lookupLink finds the project linked under the request's identifier, checking
// the target language when the extension names one.
func (s *Server) lookupLink(r *http.Request) (lookupProject, error) {
	ident := strings.TrimSpace(r.URL.Query().Get("project"))
	if ident == "" {
		return lookupProject{}, badRequest("project (the Crowdin project identifier) is required")
	}
	l, err := s.st.LinkByIdentifier(r.Context(), store.LinkCrowdin, ident)
	if err == store.ErrNotFound {
		return lookupProject{}, &httpError{code: http.StatusNotFound, msg: "no bellingua project is linked to Crowdin project " + ident}
	}
	if err != nil {
		return lookupProject{}, err
	}
	if t := r.URL.Query().Get("target"); t != "" && !strings.EqualFold(t, l.TargetLang) {
		return lookupProject{}, &httpError{code: http.StatusNotFound, msg: "the linked project holds " + l.TargetLang + ", not " + t}
	}
	p, err := s.st.Project(r.Context(), l.ProjectID)
	if err != nil {
		return lookupProject{}, err
	}
	return lookupProject{ID: p.ID, Name: p.Name, Identifier: l.Identifier, SourceLang: l.SourceLang, TargetLang: l.TargetLang}, nil
}

func (s *Server) crowdinLookup(w http.ResponseWriter, r *http.Request) error {
	lp, err := s.lookupLink(r)
	if err != nil {
		return err
	}
	text := r.URL.Query().Get("text")
	if strings.TrimSpace(text) == "" {
		return badRequest("text is required")
	}
	if utf8.RuneCountInString(text) > maxLookupText {
		return badRequest("text is longer than %d characters", maxLookupText)
	}
	limit := lookupLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLookupLimit {
			return badRequest("limit must be 1-%d", maxLookupLimit)
		}
		limit = n
	}
	ctx := r.Context()
	out := []lookupMatch{}
	seen := map[string]bool{}
	exact, err := s.st.ExactMatches(ctx, lp.ID, 0, text, limit)
	if err != nil {
		return err
	}
	for _, m := range exact {
		t := model.Plain(m.Target)
		seen[t] = true
		out = append(out, lookupMatch{100, model.Plain(m.Source), t, m.State, m.Path})
	}
	if len(out) < limit {
		ix, err := s.tm.Get(ctx, lp.ID)
		if err != nil {
			return err
		}
		fuzzy := ix.Search([]model.Piece{{Text: text}}, 0, limit*2)
		ids := make([]int64, len(fuzzy))
		for i, m := range fuzzy {
			ids[i] = m.UnitID
		}
		rows, err := s.st.TMUnits(ctx, ids)
		if err != nil {
			return err
		}
		byID := map[int64]store.TMRow{}
		for _, row := range rows {
			byID[row.UnitID] = row
		}
		for _, m := range fuzzy {
			row, ok := byID[m.UnitID]
			t := model.Plain(row.Target)
			if !ok || seen[t] || model.Plain(row.Source) == text {
				continue // stale index entry, a repeated translation, or an exact match already listed
			}
			seen[t] = true
			out = append(out, lookupMatch{m.Score, model.Plain(row.Source), t, row.State, row.Path})
			if len(out) == limit {
				break
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return ok(w, map[string]any{"project": lp, "matches": out})
}

type lookupEntry struct {
	Source string      `json:"s"`
	Target string      `json:"t"`
	State  model.State `json:"state"`
	Path   string      `json:"path"`
	Key    string      `json:"key"`
}

// crowdinLookupAll is the whole translated vocabulary of the linked project,
// for the extension to cache and match locally. It is revalidated by ETag.
func (s *Server) crowdinLookupAll(w http.ResponseWriter, r *http.Request) error {
	lp, err := s.lookupLink(r)
	if err != nil {
		return err
	}
	min := model.Translated
	switch v := strings.ToLower(r.URL.Query().Get("minState")); v {
	case "", "translated":
	case "mt":
		min = model.MT
	case "approved":
		min = model.Approved
	default:
		return badRequest("minState must be mt, translated or approved, not %q", v)
	}
	// The tag covers the filter too: a different minState is a different body.
	if etag(w, r, s.st.Rev()*8+int64(min)) {
		return nil
	}
	entries := []lookupEntry{}
	err = s.st.ExportUnits(r.Context(), store.Filter{ProjectID: lp.ID}, func(u store.ExportRow) error {
		if u.Target != nil && u.State >= min {
			entries = append(entries, lookupEntry{u.Source, *u.Target, u.State, u.Path, u.Key})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"project": lp, "minState": min, "entries": entries})
}

// extensionCORS answers browser-extension origins on the lookup routes; web
// pages get no CORS headers and stay blocked.
func extensionCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		for _, p := range []string{"moz-extension://", "chrome-extension://"} {
			if !strings.HasPrefix(o, p) || len(o) == len(p) {
				continue
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET")
				h.Set("Access-Control-Allow-Headers", "If-None-Match")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", "ETag")
		}
		next.ServeHTTP(w, r)
	})
}
