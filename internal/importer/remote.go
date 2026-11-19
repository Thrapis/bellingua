package importer

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

// RemoteUnit is one string fetched from a remote project (Crowdin). Texts are
// plain strings; there is no code detection beyond what the QA mask does.
type RemoteUnit struct {
	Key, Context string
	Source       string
	Target       string
	HasTarget    bool
	State        model.State // of the target; ignored without one
}

// RemoteFile is one source file of a remote project.
type RemoteFile struct {
	Path  string // slash-separated, no leading slash
	Units []RemoteUnit
}

// ImportFiles writes already fetched files into a project with exactly the
// merge rules of a tree import (see the package comment): human work is never
// overwritten, a changed source flags the unit, missing keys become obsolete.
// Files are stored as Crowdin CSV, so export mirrors them as such.
func ImportFiles(ctx context.Context, st *store.Store, runner *qa.Runner, projectID int64, files []RemoteFile,
	progress func(done, total int), log *slog.Logger) (Stats, error) {
	start := time.Now()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sorted := append([]RemoteFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	paths := make([]string, len(sorted))
	results := make(chan parsed, len(sorted))
	for i, f := range sorted {
		paths[i] = f.Path
		p := parsed{path: f.Path, format: CrowdinCSV, units: make([]unit, len(f.Units))}
		for j, u := range f.Units {
			x := unit{key: u.Key, context: u.Context, src: []model.Piece{{Text: u.Source}}}
			if u.HasTarget && u.Target != "" {
				x.tgt, x.state = []model.Piece{{Text: u.Target}}, u.State
			}
			x.mask = runner.Mask(qa.Unit{Source: x.src, Target: x.tgt, State: x.state})
			p.units[j] = x
		}
		results <- p
	}
	close(results)
	stats, err := writeAll(ctx, st, runner, projectID, CrowdinCSV, paths, results, progress, log)
	stats.Duration = time.Since(start)
	return stats, err
}
