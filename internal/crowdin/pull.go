package crowdin

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/model"
)

// Snapshot is what a pull read from Crowdin, ready for importer.ImportFiles.
type Snapshot struct {
	Project Project
	Lang    string // the target language pulled
	Files   []importer.RemoteFile
	Strings int // strings read
	Plural  int // plural strings left out (not supported yet)
	Missing int // strings whose file is unknown (left out)
}

// Pull reads a project's files, strings, and the translations and approvals
// into one target language. lang may be empty when the project has exactly one.
func (c *Client) Pull(ctx context.Context, remoteID int64, lang string, progress func(step string, done, total int)) (Snapshot, error) {
	say := func(s string) {
		if progress != nil {
			progress(s, 0, 0)
		}
	}
	var snap Snapshot
	say("project")
	proj, err := c.GetProject(ctx, remoteID)
	if err != nil {
		return snap, err
	}
	snap.Project = proj
	if lang == "" {
		if len(proj.TargetLangs) != 1 {
			return snap, fmt.Errorf("crowdin: project %s has %d target languages (%v); choose one", proj.Name, len(proj.TargetLangs), proj.TargetLangs)
		}
		lang = proj.TargetLangs[0]
	}
	snap.Lang = lang

	// Totals for the progress bar; unknown (0) when the token lacks the scope.
	stat, _ := c.LanguageStat(ctx, remoteID, lang)
	page := func(step string, total int) func(int) {
		return func(n int) {
			if progress != nil {
				progress(step, n, max(total, n))
			}
		}
	}
	say("files")
	files, err := c.Files(ctx, remoteID)
	if err != nil {
		return snap, err
	}
	c.Page = page("strings", stat.Strings)
	say("strings")
	strs, err := c.Strings(ctx, remoteID)
	if err != nil {
		return snap, err
	}
	c.Page = page("translations", stat.Translated)
	say("translations")
	tr, err := c.Translations(ctx, remoteID, lang)
	if err != nil {
		return snap, err
	}
	c.Page = page("approvals", stat.Approved)
	say("approvals")
	ap, err := c.Approved(ctx, remoteID, lang)
	if err != nil {
		return snap, err
	}

	c.Page = nil
	byFile := map[int64][]String{}
	for _, s := range strs {
		snap.Strings++
		if s.Plural {
			snap.Plural++
			continue
		}
		byFile[s.FileID] = append(byFile[s.FileID], s)
	}
	for _, f := range files {
		list := byFile[f.ID]
		delete(byFile, f.ID)
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		rf := importer.RemoteFile{Path: f.Path, Units: make([]importer.RemoteUnit, 0, len(list))}
		seen := map[string]bool{}
		for _, s := range list {
			key := s.Identifier
			if key == "" || seen[key] {
				// No identifier, or one shared by two strings: the string id is
				// unique and stable, so a re-pull finds the same unit again.
				key += "#" + strconv.FormatInt(s.ID, 10)
				key = trimHash(key)
			}
			seen[key] = true
			u := importer.RemoteUnit{Key: key, Context: s.Context, Source: s.Text}
			if t, ok := tr[s.ID]; ok && t != "" {
				u.Target, u.HasTarget, u.State = t, true, model.Translated
				if ap[s.ID] {
					u.State = model.Approved
				}
			}
			rf.Units = append(rf.Units, u)
		}
		snap.Files = append(snap.Files, rf)
	}
	for _, rest := range byFile {
		snap.Missing += len(rest)
	}
	return snap, nil
}

// trimHash turns "#12" (an empty identifier) into "id:12".
func trimHash(k string) string {
	if len(k) > 0 && k[0] == '#' {
		return "id:" + k[1:]
	}
	return k
}
