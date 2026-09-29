// Command bellingua is a local translation workbench: an HTTP server with an
// embedded web editor over a SQLite database, plus CLI import/export/backup.
//
//	bellingua                  (no arguments, e.g. a double-click: serve -open, data next to the exe)
//	bellingua serve [-open]
//	bellingua import  -project my-app -in <xliff-tree>
//	bellingua import  -project my-app -in <xliff-en-tree> -extra   (reference language)
//	bellingua export  -project my-app -out <dir> [-format xliff|crowdin-csv] [-min-state mt]
//	bellingua qa      -project my-app
//	bellingua backup
//	bellingua restore [-from latest|<name>]
//	bellingua projects
//
// Every command takes -config (default bellingua.yaml, optional) and -db.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/exporter"
	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/importer"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
	"github.com/Thrapis/bellingua/internal/store"
)

var commands = map[string]func(ctx context.Context, args []string) error{
	"serve":    cmdServe,
	"import":   cmdImport,
	"export":   cmdExport,
	"qa":       cmdQA,
	"backup":   cmdBackup,
	"restore":  cmdRestore,
	"projects": cmdProjects,
}

// launched is set when bellingua runs without arguments (see main).
var launched bool

func main() {
	args := os.Args[1:]
	// Without arguments (a double-click, a shortcut) bellingua runs the
	// editor with its data next to the executable, whatever the working
	// directory is.
	launched = len(args) == 0
	if launched {
		args = []string{"serve", "-open"}
		if err := chdirToExe(); err != nil {
			fail("bellingua: %v\n", err)
		}
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage()
		return
	}
	if commands[args[0]] == nil {
		usage()
		os.Exit(2)
	}
	name := args[0]
	// SIGTERM also covers closing the console window on Windows, so the
	// Lingvanex server and a final backup still get a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, name, args[1:]); err != nil {
		stop()
		fail("bellingua %s: %v\n", name, err)
	}
}

// fail prints the error and exits. When the process has a console window of
// its own (started from Explorer), it waits for Enter first, or the window
// would close before the error can be read.
func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
	if ownsConsole() {
		fmt.Fprint(os.Stderr, "Press Enter to close.")
		fmt.Scanln()
	}
	os.Exit(1)
}

// chdirToExe makes the executable's folder the working directory, so the
// default bellingua.yaml, bellingua.db and relative paths in the config are
// found there.
func chdirToExe() error {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	return os.Chdir(filepath.Dir(exe))
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: bellingua <command> [flags]

Without arguments, bellingua runs "serve -open" with bellingua.yaml and
bellingua.db in the executable's folder.

commands:
  serve      run the web editor
  import     import an XLIFF / Crowdin CSV tree into a project (-extra: a reference language)
  export     export a project as a file tree
  qa         recompute QA checks of a project
  backup     snapshot the database to the configured targets
  restore    replace the database with a backup (server must be stopped)
  projects   list projects

run "bellingua <command> -h" for the command's flags
`)
}

// env is what every command gets after the common flags are parsed.
type env struct {
	cfg config.Config
	log *slog.Logger
}

// commonFlags registers -config and -db on fs and returns a loader.
func commonFlags(fs *flag.FlagSet) func() (*env, error) {
	cfgPath := fs.String("config", "bellingua.yaml", "config file (optional)")
	db := fs.String("db", "", "database file (overrides config)")
	return func() (*env, error) {
		explicit := isSet(fs, "config")
		cfg, err := config.Load(*cfgPath, !explicit)
		if err != nil {
			return nil, err
		}
		if *db != "" {
			cfg.DB = *db
		}
		return &env{cfg: cfg, log: newLogger(cfg.LogLevel)}, nil
	}
}

func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

func run(ctx context.Context, name string, args []string) error { return commands[name](ctx, args) }

func openStore(ctx context.Context, e *env) (*store.Store, error) {
	if dir := filepath.Dir(e.cfg.DB); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return store.Open(ctx, e.cfg.DB)
}

// projectRunner builds the QA runner for a project, glossary included.
func projectRunner(ctx context.Context, st *store.Store, p *store.Project) (*qa.Runner, error) {
	g, err := st.Glossary(ctx, p)
	if err != nil {
		return nil, err
	}
	return qa.NewRunner(p.Settings.QA).WithGlossary(g), nil
}

func cmdImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	load := commonFlags(fs)
	project := fs.String("project", "", "project name (created if missing)")
	in := fs.String("in", "", "directory to import")
	format := fs.String("format", "", "xliff | crowdin-csv (default: by file extension)")
	srcLang := fs.String("source-lang", "", "source language for a new project (default: from the XLIFF files)")
	tgtLang := fs.String("target-lang", "", "target language for a new project (default: from the XLIFF files)")
	extra := fs.Bool("extra", false, "import an extra (reference) language of an existing project: an XLIFF tree laid out like the main import")
	lang := fs.String("lang", "", "with -extra: the language code (default: the files' target-language)")
	fs.Parse(args)
	if *project == "" || *in == "" {
		return errors.New("-project and -in are required")
	}
	e, err := load()
	if err != nil {
		return err
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()

	p, err := st.ProjectByName(ctx, *project)
	if *extra {
		if err != nil {
			return err
		}
		stats, err := importer.ImportLang(ctx, st, importer.LangOptions{ProjectID: p.ID, Root: *in, Lang: *lang, Log: e.log})
		if err != nil {
			return err
		}
		e.log.Info("extra language imported", "lang", stats.Lang, "files", stats.Files, "stored", stats.Stored,
			"outdated", stats.Outdated, "noTarget", stats.NoTarget, "unknownFiles", stats.UnknownFiles,
			"unknownUnits", stats.UnknownUnits, "took", stats.Duration.Round(1e6))
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		src, tgt := *srcLang, *tgtLang
		if src == "" || tgt == "" {
			s, t, err := detectLangs(*in)
			if err != nil {
				return fmt.Errorf("new project needs -source-lang and -target-lang: %w", err)
			}
			src, tgt = nonEmpty(src, s), nonEmpty(tgt, t)
		}
		if p, err = st.CreateProject(ctx, *project, src, tgt); err != nil {
			return err
		}
		e.log.Info("project created", "name", p.Name, "source", p.SourceLang, "target", p.TargetLang)
	} else if err != nil {
		return err
	}

	runner, err := projectRunner(ctx, st, p)
	if err != nil {
		return err
	}
	last := -1
	stats, err := importer.Import(ctx, st, runner, importer.Options{
		ProjectID: p.ID, Root: *in, Format: *format, Log: e.log,
		Progress: func(done, total int) {
			if pct := done * 100 / total; pct/10 != last/10 {
				last = pct
				e.log.Info("importing", "files", fmt.Sprintf("%d/%d", done, total))
			}
		},
	})
	if err != nil {
		return err
	}
	e.log.Info("import done", "files", stats.Files, "units", stats.Units, "inserted", stats.Inserted,
		"updated", stats.Updated, "sourceChanged", stats.SourceChanged, "obsolete", stats.Obsolete,
		"brokenTargets", stats.Problems, "took", stats.Duration.Round(1e6))
	return nil
}

func nonEmpty(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// detectLangs reads the language pair from the first XLIFF file under root.
func detectLangs(root string) (string, string, error) {
	var src, tgt string
	stop := errors.New("stop")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".xlf" && ext != ".xliff" {
			return nil
		}
		fh, err := os.Open(p)
		if err != nil {
			return err
		}
		defer fh.Close()
		f, _, err := xliff.Read(fh)
		if err != nil {
			return err
		}
		src, tgt = f.SourceLang, f.TargetLang
		return stop
	})
	if err != nil && !errors.Is(err, stop) {
		return "", "", err
	}
	if src == "" || tgt == "" {
		return "", "", errors.New("no XLIFF file with source-language/target-language found")
	}
	return src, tgt, nil
}

func cmdExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	load := commonFlags(fs)
	project := fs.String("project", "", "project name")
	out := fs.String("out", "", "output directory")
	format := fs.String("format", "", "xliff | crowdin-csv (default: each file's imported format)")
	minState := fs.String("min-state", "mt", "lowest state exported as a translation: mt | translated | approved")
	fs.Parse(args)
	if *project == "" || *out == "" {
		return errors.New("-project and -out are required")
	}
	ms, ok := model.ParseState(*minState)
	if !ok || ms == model.Untranslated {
		return fmt.Errorf("-min-state %q: want mt, translated or approved", *minState)
	}
	e, err := load()
	if err != nil {
		return err
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := st.ProjectByName(ctx, *project)
	if err != nil {
		return fmt.Errorf("project %q: %w", *project, err)
	}
	stats, err := exporter.Export(ctx, st, exporter.Options{ProjectID: p.ID, Out: *out, Format: *format, MinState: ms})
	if err != nil {
		return err
	}
	e.log.Info("export done", "files", stats.Files, "units", stats.Units, "translated", stats.Translated,
		"dropped", stats.Dropped, "took", stats.Duration.Round(1e6))
	return nil
}

func cmdQA(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("qa", flag.ExitOnError)
	load := commonFlags(fs)
	project := fs.String("project", "", "project name")
	fs.Parse(args)
	if *project == "" {
		return errors.New("-project is required")
	}
	e, err := load()
	if err != nil {
		return err
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := st.ProjectByName(ctx, *project)
	if err != nil {
		return fmt.Errorf("project %q: %w", *project, err)
	}
	runner, err := projectRunner(ctx, st, p)
	if err != nil {
		return err
	}
	n, err := st.RecheckQA(ctx, p.ID, runner, nil)
	if err != nil {
		return err
	}
	e.log.Info("qa done", "changed", n)
	return nil
}

func cmdProjects(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("projects", flag.ExitOnError)
	load := commonFlags(fs)
	fs.Parse(args)
	e, err := load()
	if err != nil {
		return err
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()
	ps, err := st.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		c := p.Counts
		fmt.Printf("%-20s %s→%s  files=%d units=%d mt=%d translated=%d approved=%d errors=%d warnings=%d\n",
			p.Name, p.SourceLang, p.TargetLang, p.Files, c.Total, c.MT, c.Translated, c.Approved, c.Errors, c.Warnings)
	}
	return nil
}
