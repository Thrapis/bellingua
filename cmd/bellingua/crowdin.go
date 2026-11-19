package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/Thrapis/bellingua/internal/crowdin"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/store"
)

// cmdCrowdin links a project to a Crowdin project and pulls it (read-only
// towards Crowdin). The project is created when missing.
func cmdCrowdin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("crowdin", flag.ExitOnError)
	load := commonFlags(fs)
	project := fs.String("project", "", "project name (created if missing)")
	remote := fs.Int64("remote", 0, "Crowdin project id (needed the first time; later it is remembered)")
	org := fs.String("org", "", "Crowdin Enterprise organization (default: crowdin.com)")
	target := fs.String("target", "", "Crowdin target language id, e.g. be (optional when the project has one)")
	fs.Parse(args)
	if *project == "" {
		return errors.New("-project is required")
	}
	e, err := load()
	if err != nil {
		return err
	}
	if e.cfg.Crowdin.Token == "" {
		return errors.New("no Crowdin token: set crowdin.token in the config or $" + crowdin.TokenEnv + " (a personal access token with read-only scopes)")
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()

	p, err := st.ProjectByName(ctx, *project)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var link store.Link
	if p != nil {
		if link, err = st.Link(ctx, p.ID, store.LinkCrowdin); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if *remote != 0 || link.RemoteID == 0 {
		if *remote == 0 {
			return errors.New("-remote is required: this project is not linked to Crowdin yet")
		}
		base, err := crowdin.BaseForOrg(*org)
		if err != nil {
			return err
		}
		c := &crowdin.Client{Base: nonEmpty(base, e.cfg.Crowdin.BaseURL), Token: e.cfg.Crowdin.Token}
		rp, err := c.GetProject(ctx, *remote)
		if err != nil {
			return err
		}
		lang := *target
		if lang == "" {
			if len(rp.TargetLangs) != 1 {
				return fmt.Errorf("the Crowdin project has target languages %v: choose one with -target", rp.TargetLangs)
			}
			lang = rp.TargetLangs[0]
		}
		if p == nil {
			if p, err = st.CreateProject(ctx, *project, rp.SourceLang, lang); err != nil {
				return err
			}
			e.log.Info("project created", "name", p.Name, "source", p.SourceLang, "target", p.TargetLang)
		}
		link = store.Link{ProjectID: p.ID, Kind: store.LinkCrowdin, RemoteID: *remote, BaseURL: base,
			Identifier: rp.Identifier, SourceLang: rp.SourceLang, TargetLang: lang}
		if err := st.SetLink(ctx, link); err != nil {
			return err
		}
	}

	c := &crowdin.Client{Base: nonEmpty(link.BaseURL, e.cfg.Crowdin.BaseURL), Token: e.cfg.Crowdin.Token}
	snap, err := c.Pull(ctx, link.RemoteID, link.TargetLang, func(step string) { e.log.Info("crowdin", "reading", step) })
	if err != nil {
		return err
	}
	runner, err := projectRunner(ctx, st, p)
	if err != nil {
		return err
	}
	stats, err := importer.ImportFiles(ctx, st, runner, p.ID, snap.Files, nil, e.log)
	if err != nil {
		return err
	}
	if err := st.MarkPulled(ctx, p.ID, store.LinkCrowdin, 0); err != nil {
		return err
	}
	e.log.Info("pull done", "crowdin", snap.Project.Identifier, "lang", snap.Lang, "files", stats.Files, "units", stats.Units,
		"inserted", stats.Inserted, "updated", stats.Updated, "sourceChanged", stats.SourceChanged, "obsolete", stats.Obsolete,
		"pluralSkipped", snap.Plural, "took", stats.Duration.Round(1e6))
	return nil
}
