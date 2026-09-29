package importer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Thrapis/bellingua/internal/formats/xliff"
	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

func writeLangXLIFF(t *testing.T, dir, rel, lang string, units ...xliff.Unit) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := xliff.Write(fh, &xliff.File{Original: rel, SourceLang: "ru", TargetLang: lang, Units: units}); err != nil {
		t.Fatal(err)
	}
}

func refUnit(id string, src, tgt []model.Piece) xliff.Unit {
	return xliff.Unit{ID: id, Source: src, Target: tgt}
}

func TestImportLang(t *testing.T) {
	ctx, st, p, runner := setup(t)
	main := t.TempDir()
	writeXLIFF(t, main, "a/one.xlf",
		mtUnit("x", "Привет", "Прывітанне"),
		xliff.Unit{ID: "y", Source: []model.Piece{tx("Нажмите "), cd("{KEY}")}},
		mtUnit("z", "Старый текст", "Стары тэкст"),
	)
	if _, err := Import(ctx, st, runner, Options{ProjectID: p.ID, Root: main}); err != nil {
		t.Fatal(err)
	}

	en := t.TempDir()
	writeLangXLIFF(t, en, "a/one.xlf", "en-US",
		refUnit("x", []model.Piece{tx("Привет")}, []model.Piece{tx("Hello")}),
		refUnit("y", []model.Piece{tx("Нажмите "), cd("{KEY}")}, []model.Piece{tx("Press "), cd("{KEY}")}),
		refUnit("z", []model.Piece{tx("Совсем старый текст")}, []model.Piece{tx("Old text")}), // other source: outdated
		refUnit("gone", []model.Piece{tx("Нет")}, []model.Piece{tx("No")}),                    // unknown key
		refUnit("empty", []model.Piece{tx("Пусто")}, nil),                                     // no target
	)
	writeLangXLIFF(t, en, "b/extra.xlf", "en-US", refUnit("q", []model.Piece{tx("Q")}, []model.Piece{tx("Q")})) // unknown file

	stats, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: en})
	if err != nil {
		t.Fatal(err)
	}
	want := LangStats{Lang: "en-US", Files: 2, Stored: 3, Outdated: 1, NoTarget: 1, UnknownFiles: 1, UnknownUnits: 1}
	stats.Duration = 0
	if stats != want {
		t.Errorf("stats = %+v", stats)
	}

	y := unitByKey(t, ctx, st, p.ID, "y")
	ls, err := st.UnitLangs(ctx, y.ID)
	if err != nil || len(ls) != 1 {
		t.Fatalf("langs = %+v %v", ls, err)
	}
	if l := ls[0]; l.Lang != "en-US" || !l.Locked || l.Outdated || l.State != model.Approved ||
		len(l.Target) != 2 || !l.Target[1].Code || l.Target[1].Text != "{KEY}" {
		t.Errorf("y = %+v", l)
	}
	if ls, _ := st.UnitLangs(ctx, unitByKey(t, ctx, st, p.ID, "z").ID); len(ls) != 1 || !ls[0].Outdated {
		t.Errorf("z not outdated: %+v", ls)
	}

	// Re-import replaces the language's texts (x is gone now).
	en2 := t.TempDir()
	writeLangXLIFF(t, en2, "a/one.xlf", "en-US", refUnit("y", []model.Piece{tx("Нажмите "), cd("{KEY}")}, []model.Piece{tx("Hit "), cd("{KEY}")}))
	if _, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: en2}); err != nil {
		t.Fatal(err)
	}
	if ls, _ := st.UnitLangs(ctx, unitByKey(t, ctx, st, p.ID, "x").ID); len(ls) != 0 {
		t.Errorf("x kept a stale text: %+v", ls)
	}
	langs, _ := st.ProjectLangs(ctx, p.ID)
	if len(langs) != 1 || langs[0].Units != 1 {
		t.Errorf("project langs = %+v", langs)
	}

	// The project's own languages and mixed trees are refused.
	be := t.TempDir()
	writeLangXLIFF(t, be, "a/one.xlf", "be", refUnit("x", []model.Piece{tx("Привет")}, []model.Piece{tx("Прывітанне")}))
	if _, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: be}); !errors.Is(err, store.ErrMainLang) {
		t.Errorf("main language accepted: %v", err)
	}
	mixed := t.TempDir()
	writeLangXLIFF(t, mixed, "a/one.xlf", "uk", refUnit("x", []model.Piece{tx("Привет")}, []model.Piece{tx("Привіт")}))
	writeLangXLIFF(t, mixed, "b/two.xlf", "en", refUnit("x", []model.Piece{tx("Привет")}, []model.Piece{tx("Hi")}))
	if _, err := ImportLang(ctx, st, LangOptions{ProjectID: p.ID, Root: mixed, Workers: 1}); err == nil {
		t.Error("mixed languages accepted")
	}

	if err := st.DeleteProjectLang(ctx, p.ID, "en-US"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := st.UnitLangs(ctx, y.ID); len(ls) != 0 {
		t.Errorf("texts survived deleting the language: %+v", ls)
	}
}
