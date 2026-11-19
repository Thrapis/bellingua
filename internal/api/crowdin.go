package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Thrapis/bellingua/internal/crowdin"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/store"
)

// Crowdin links: a project pulled from a Crowdin project, and pulled again on
// demand. Read-only towards Crowdin; the token lives in the config or the
// environment and is never stored or returned.

func (s *Server) crowdinClient(l store.Link) (*crowdin.Client, error) {
	if s.cfg.Crowdin.Token == "" {
		return nil, &httpError{code: http.StatusPreconditionFailed,
			msg: "No Crowdin token: set crowdin.token in the config file or the BELLINGUA_CROWDIN_TOKEN environment variable (a personal access token with read-only scopes) and restart."}
	}
	base := l.BaseURL
	if base == "" {
		base = s.cfg.Crowdin.BaseURL
	}
	return &crowdin.Client{Base: base, Token: s.cfg.Crowdin.Token}, nil
}

// crowdinFail makes a Crowdin answer readable to the client: the user's own
// token problems are 4xx for them, not a bellingua fault.
func crowdinFail(err error) error {
	if ce, ok := err.(*crowdin.Error); ok {
		code := http.StatusBadGateway
		if ce.Status == http.StatusUnauthorized || ce.Status == http.StatusForbidden || ce.Status == http.StatusNotFound {
			code = http.StatusUnprocessableEntity
		}
		return &httpError{code: code, msg: ce.Error()}
	}
	return err
}

type crowdinLinkBody struct {
	RemoteID     int64  `json:"remoteId"`
	Organization string `json:"organization"` // Crowdin Enterprise only
	TargetLang   string `json:"targetLang"`   // Crowdin language id, e.g. be; optional with one target language
}

// resolveLink checks the remote project with Crowdin and builds the link.
func (s *Server) resolveLink(ctx context.Context, projectID int64, b crowdinLinkBody) (store.Link, crowdin.Project, error) {
	l := store.Link{ProjectID: projectID, Kind: store.LinkCrowdin, RemoteID: b.RemoteID}
	if b.RemoteID <= 0 {
		return l, crowdin.Project{}, badRequest("remoteId (the Crowdin project id) is required")
	}
	var err error
	if l.BaseURL, err = crowdin.BaseForOrg(b.Organization); err != nil {
		return l, crowdin.Project{}, badRequest("%v", err)
	}
	c, err := s.crowdinClient(l)
	if err != nil {
		return l, crowdin.Project{}, err
	}
	rp, err := c.GetProject(ctx, b.RemoteID)
	if err != nil {
		return l, rp, crowdinFail(err)
	}
	l.Identifier, l.SourceLang, l.TargetLang = rp.Identifier, rp.SourceLang, b.TargetLang
	if l.TargetLang == "" {
		if len(rp.TargetLangs) != 1 {
			return l, rp, badRequest("the Crowdin project has target languages %v: choose one with targetLang", rp.TargetLangs)
		}
		l.TargetLang = rp.TargetLangs[0]
	}
	return l, rp, nil
}

func (s *Server) getCrowdin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	out := map[string]any{"tokenSet": s.cfg.Crowdin.Token != "", "link": nil}
	switch l, err := s.st.Link(r.Context(), p.ID, store.LinkCrowdin); err {
	case nil:
		out["link"] = l
	case store.ErrNotFound:
	default:
		return err
	}
	return ok(w, out)
}

func (s *Server) putCrowdin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	var b crowdinLinkBody
	if err := decode(r, &b); err != nil {
		return err
	}
	l, _, err := s.resolveLink(r.Context(), p.ID, b)
	if err != nil {
		return err
	}
	if err := s.st.SetLink(r.Context(), l); err != nil {
		return err
	}
	l, err = s.st.Link(r.Context(), p.ID, store.LinkCrowdin)
	if err != nil {
		return err
	}
	return ok(w, l)
}

// crowdinProjects copies a Crowdin project: creates the project, links it and
// starts the first pull. The answer is the pull job (its "project" is the new id).
func (s *Server) crowdinProjects(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		crowdinLinkBody
		Name string `json:"name"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	l, rp, err := s.resolveLink(r.Context(), 0, b.crowdinLinkBody)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = rp.Name
	}
	if name == "" {
		name = rp.Identifier
	}
	if _, err := s.st.ProjectByName(r.Context(), name); err == nil {
		return &httpError{code: http.StatusConflict, msg: "a project named " + name + " exists; choose another name"}
	}
	p, err := s.st.CreateProject(r.Context(), name, l.SourceLang, l.TargetLang)
	if err != nil {
		return err
	}
	l.ProjectID = p.ID
	if err := s.st.SetLink(r.Context(), l); err != nil {
		return err
	}
	return s.startPull(w, p, l)
}

func (s *Server) pullCrowdin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.project(r)
	if err != nil {
		return err
	}
	l, err := s.st.Link(r.Context(), p.ID, store.LinkCrowdin)
	if err == store.ErrNotFound {
		return badRequest("this project is not linked to a Crowdin project")
	}
	if err != nil {
		return err
	}
	return s.startPull(w, p, l)
}

// PullResult is the job result of a pull.
type PullResult struct {
	importer.Stats
	Lang   string `json:"lang"`
	Plural int    `json:"pluralSkipped"` // plural strings are not supported yet
}

func (s *Server) startPull(w http.ResponseWriter, p *store.Project, l store.Link) error {
	c, err := s.crowdinClient(l)
	if err != nil {
		return err
	}
	runner, _, err := s.runner(context.Background(), p)
	if err != nil {
		return err
	}
	return s.startOnce(w, "crowdin-pull", p, func(ctx context.Context, pr Progress) (any, error) {
		defer s.tm.Invalidate(p.ID)
		snap, err := c.Pull(ctx, l.RemoteID, l.TargetLang, func(step string, done, total int) {
			pr.Update("", "Crowdin: "+step, int64(done), int64(total), 0)
		})
		if err != nil {
			return nil, crowdinFail(err)
		}
		pr.Say("Writing %d files", len(snap.Files))
		stats, err := importer.ImportFiles(ctx, s.st, runner, p.ID, snap.Files,
			func(done, total int) { pr.Set(int64(done), int64(total)) }, s.log)
		if err != nil {
			return nil, err
		}
		if err := s.st.MarkPulled(ctx, p.ID, store.LinkCrowdin, time.Now().Unix()); err != nil {
			return nil, err
		}
		return PullResult{Stats: stats, Lang: snap.Lang, Plural: snap.Plural}, nil
	})
}
