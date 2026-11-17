package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/store"
)

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// TestReplaceInTargetsCorpus times replace_in_targets on a real corpus:
// BELLINGUA_BENCH_DB=path/to.db BELLINGUA_BENCH_FIND=word go test -run Corpus -v ./internal/api
// The database is copied (with its -wal) into a temp dir first, because the
// real run writes; the original is only read as bytes, never opened.
func TestReplaceInTargetsCorpus(t *testing.T) {
	src := os.Getenv("BELLINGUA_BENCH_DB")
	if src == "" {
		t.Skip("BELLINGUA_BENCH_DB not set")
	}
	find := os.Getenv("BELLINGUA_BENCH_FIND")
	if find == "" {
		t.Skip("BELLINGUA_BENCH_FIND not set")
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	copyFile(t, src, dst)
	if _, err := os.Stat(src + "-wal"); err == nil {
		copyFile(t, src+"-wal", dst+"-wal")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, err := st.Projects(ctx)
	if err != nil || len(ps) == 0 {
		t.Fatalf("no project: %v", err)
	}
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	s := New(ctx, st, config.Default(), nil, bm, nil, slog.New(slog.DiscardHandler))

	run := func(dry bool, repl string) mcpSweep {
		t0 := time.Now()
		out, err := s.replaceInTargets(ctx, replaceIn{filterIn: filterIn{Project: ps[0].ID}, Pairs: []replacePair{{Find: find, Replace: repl}}, DryRun: &dry, Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("dryRun=%v: %v, matched %d changed %d skipped %d failed %d next=%q", dry, time.Since(t0).Round(time.Millisecond), out.Matched, out.Changed, out.Skipped, out.Failed, out.Next)
		return out
	}
	run(true, find+"~") // cold
	run(true, find+"~")
	run(true, find+"~")
	run(false, "‹"+find+"›")
}

// TestStepCCorpus times list_words, export_units and a two-pair sweep with no
// query on a real corpus (a copy, as above; exports land next to the copy):
// BELLINGUA_BENCH_DB=path/to.db go test -run StepCCorpus -v ./internal/api
func TestStepCCorpus(t *testing.T) {
	src := os.Getenv("BELLINGUA_BENCH_DB")
	if src == "" {
		t.Skip("BELLINGUA_BENCH_DB not set")
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	copyFile(t, src, dst)
	if _, err := os.Stat(src + "-wal"); err == nil {
		copyFile(t, src+"-wal", dst+"-wal")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ps, err := st.Projects(ctx)
	if err != nil || len(ps) == 0 {
		t.Fatalf("no project: %v", err)
	}
	bm, _ := backup.NewManager(st, config.Backup{}, slog.New(slog.DiscardHandler))
	cfg := config.Default()
	cfg.DB = dst
	s := New(ctx, st, cfg, nil, bm, nil, slog.New(slog.DiscardHandler))
	pid := ps[0].ID
	timed := func(name string, fn func()) {
		t0 := time.Now()
		fn()
		t.Logf("%s: %v", name, time.Since(t0).Round(time.Millisecond))
	}
	for i := 0; i < 2; i++ { // cold, warm
		timed("list_words (all targets)", func() {
			out, err := s.listWords(ctx, wordsIn{filterIn: filterIn{Project: pid}})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("  %d units, %d distinct words, top %+v", out.Units, out.Total, out.Words[:3])
		})
	}
	timed("list_words maxCount 1, regex-filtered", func() {
		if _, err := s.listWords(ctx, wordsIn{filterIn: filterIn{Project: pid, Query: `ў\b`, In: "target", Regex: true}, MaxCount: 1}); err != nil {
			t.Fatal(err)
		}
	})
	timed("export_units (whole project)", func() {
		out, err := s.exportUnits(ctx, exportIn{filterIn: filterIn{Project: pid}, Path: "all.tsv"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  %d rows, %.1f MB", out.Rows, float64(out.Bytes)/1e6)
	})
	timed("export_units lastOrigin ai", func() {
		out, err := s.exportUnits(ctx, exportIn{filterIn: filterIn{Project: pid, LastOrigin: "ai"}, Path: "ai.tsv"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  %d rows", out.Rows)
	})
	dry := true
	timed("replace_in_targets, two pairs, no query (dry run)", func() {
		out, err := s.replaceInTargets(ctx, replaceIn{filterIn: filterIn{Project: pid}, DryRun: &dry, Limit: 500,
			Pairs: []replacePair{{Find: "Орбита", Replace: "X"}, {Find: "Ў", Replace: "Y"}}, IgnoreCase: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  matched %d next=%q", out.Matched, out.Next)
	})
}
