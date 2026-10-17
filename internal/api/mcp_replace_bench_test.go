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
