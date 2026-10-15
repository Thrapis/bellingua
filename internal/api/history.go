package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// historyOrigins are the actors the feed tells apart. ai (an MCP agent) and mt
// (machine translation) are different actors.
var historyOrigins = map[string]bool{"edit": true, "mt": true, "tm": true, "ai": true, "bulk": true, "import": true}

func int64Param(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, badRequest("bad %s", name)
	}
	return n, nil
}

// historyDays answers the day > batch summary of the project history.
// tz is the viewer's offset east of UTC in seconds, before the unix time the
// previous page ended at.
func (s *Server) historyDays(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	tz, err := int64Param(r, "tz")
	if err != nil {
		return err
	}
	before, err := int64Param(r, "before")
	if err != nil {
		return err
	}
	days := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 31 {
		days = v
	}
	out, next, err := s.st.HistoryDays(r.Context(), p.ID, tz, before, days)
	if err != nil {
		return err
	}
	if out == nil {
		out = []store.HistoryDay{}
	}
	return ok(w, struct {
		Days []store.HistoryDay `json:"days"`
		Next int64              `json:"next"`
	}{out, next})
}

// historyList answers the paged change feed.
func (s *Server) historyList(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.HistoryFilter{ProjectID: p.ID, Batch: q.Get("batch"), Dir: strings.Trim(q.Get("dir"), "/")}
	if f.From, err = int64Param(r, "from"); err != nil {
		return err
	}
	if f.To, err = int64Param(r, "to"); err != nil {
		return err
	}
	if f.FileID, err = int64Param(r, "file"); err != nil {
		return err
	}
	if v := q.Get("origin"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if !historyOrigins[o] {
				return badRequest("unknown origin %q", o)
			}
			f.Origins = append(f.Origins, o)
		}
	}
	if v := q.Get("state"); v != "" {
		st, okState := model.ParseState(v)
		if !okState {
			return badRequest("unknown state %q", v)
		}
		f.State = &st
	}
	limit := 100
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	after, err := parseCursor(q.Get("after"))
	if err != nil {
		return err
	}
	rows, next, err := s.st.HistoryList(r.Context(), f, after, limit)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []store.HistoryRow{}
	}
	return ok(w, struct {
		Rows []store.HistoryRow `json:"rows"`
		Next *store.Cursor      `json:"next"`
	}{rows, next})
}
