package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// tmSuggestion is one translation-memory match shown in the editor.
type tmSuggestion struct {
	store.TMRow
	Score int `json:"score"` // percent; 100 = identical source
}

const tmLimit = 5

// unitTM returns translation-memory suggestions for a unit and how many
// identical strings still lack a human translation.
func (s *Server) unitTM(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.st.Unit(ctx, id, false)
	if err != nil {
		return err
	}
	src := model.Plain(u.Source)

	out := []tmSuggestion{}
	seen := map[string]bool{} // translations already offered
	exact, err := s.st.ExactMatches(ctx, u.ProjectID, u.ID, src, tmLimit)
	if err != nil {
		return err
	}
	for _, m := range exact {
		seen[model.Plain(m.Target)] = true
		out = append(out, tmSuggestion{m, 100})
	}

	if len(out) < tmLimit {
		ix, err := s.tm.Get(ctx, u.ProjectID)
		if err != nil {
			return err
		}
		fuzzy := ix.Search(u.Source, u.ID, tmLimit*2)
		ids := make([]int64, len(fuzzy))
		for i, m := range fuzzy {
			ids[i] = m.UnitID
		}
		rows, err := s.st.TMUnits(ctx, ids)
		if err != nil {
			return err
		}
		byID := map[int64]store.TMRow{}
		for _, r := range rows {
			byID[r.UnitID] = r
		}
		for _, m := range fuzzy {
			r, ok := byID[m.UnitID]
			if !ok || seen[model.Plain(r.Target)] || model.Plain(r.Source) == src {
				continue // stale index entry, duplicate offer, or an exact match already listed
			}
			seen[model.Plain(r.Target)] = true
			out = append(out, tmSuggestion{r, m.Score})
			if len(out) == tmLimit {
				break
			}
		}
	}

	dups, err := s.st.PendingDuplicates(ctx, u.ProjectID, u.ID, src)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"matches": out, "duplicates": len(dups)})
}

// propagate copies a unit's human translation to every identical string
// that has none yet.
func (s *Server) propagate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.st.Unit(ctx, id, false)
	if err != nil {
		return err
	}
	if u.Target == nil || u.State < model.Translated {
		return badRequest("the string has no human translation to propagate")
	}
	p, err := s.st.Project(ctx, u.ProjectID)
	if err != nil {
		return err
	}
	runner, _, err := s.runner(ctx, p)
	if err != nil {
		return err
	}
	ids, err := s.st.PendingDuplicates(ctx, u.ProjectID, u.ID, model.Plain(u.Source))
	if err != nil {
		return err
	}
	target := model.Plain(u.Target)
	n := 0
	for _, dup := range ids {
		saved, err := s.st.SaveUnit(ctx, dup, store.Save{Target: &target, State: model.Translated, Origin: "tm", IfBelow: model.Translated}, runner)
		if errors.Is(err, store.ErrSkipped) {
			continue
		}
		if err != nil {
			return err
		}
		s.tm.Update(u.ProjectID, saved.ID, saved.Source, true)
		n++
	}
	return ok(w, map[string]int{"changed": n})
}

// tmFillJob fills every untranslated/machine string matching the filter
// that has a 100% translation-memory match.
func (s *Server) tmFillJob(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	f, err := filter(p, r)
	if err != nil {
		return err
	}
	runner, _, err := s.runner(r.Context(), p)
	if err != nil {
		return err
	}
	return s.startOnce(w, "tm", p, func(ctx context.Context, pr Progress) (any, error) {
		pr.Say("looking for 100%% matches")
		cands, err := s.st.ExactFillCandidates(ctx, f)
		if err != nil {
			return nil, err
		}
		n := 0
		for i, c := range cands {
			t := c.Target
			_, err := s.st.SaveUnit(ctx, c.UnitID, store.Save{Target: &t, State: model.Translated, Origin: "tm", IfBelow: model.Translated}, runner)
			switch {
			case errors.Is(err, store.ErrSkipped):
			case err != nil:
				return map[string]int{"filled": n}, err
			default:
				n++
			}
			if i%50 == 0 {
				pr.Set(int64(i+1), int64(len(cands)))
			}
		}
		pr.Set(int64(len(cands)), int64(len(cands)))
		s.tm.Invalidate(p.ID)
		return map[string]int{"filled": n}, nil
	})
}
