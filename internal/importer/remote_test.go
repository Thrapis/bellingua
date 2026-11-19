package importer

import (
	"testing"

	"github.com/Thrapis/bellingua/internal/store"

	"github.com/Thrapis/bellingua/internal/model"
)

// ImportFiles follows the tree import's merge rules: a first pull creates the
// files and units with the states Crowdin gave, a second identical pull
// changes nothing, human work survives a pull, a source that disappeared is
// hidden, never deleted.
func TestImportFilesMerge(t *testing.T) {
	ctx, st, p, runner := setup(t)
	pull := func(files ...RemoteFile) Stats {
		t.Helper()
		s, err := ImportFiles(ctx, st, runner, p.ID, files, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	f := func(units ...RemoteUnit) RemoteFile { return RemoteFile{Path: "main/en.xlf", Units: units} }
	hello := RemoteUnit{Key: "hello", Context: "greeting", Source: "Hello", Target: "Прывітанне", HasTarget: true, State: model.Approved}
	bye := RemoteUnit{Key: "bye", Source: "Bye"}
	more := RemoteUnit{Key: "more", Source: "More", Target: "Больш", HasTarget: true, State: model.Translated}

	s := pull(f(hello, bye, more))
	if s.Files != 1 || s.Inserted != 3 {
		t.Fatalf("first pull = %+v", s)
	}
	for key, want := range map[string]model.State{"hello": model.Approved, "bye": model.Untranslated, "more": model.Translated} {
		if got := unitByKey(t, ctx, st, p.ID, key).State; got != want {
			t.Errorf("%s state = %v, want %v", key, got, want)
		}
	}
	files, _ := st.Files(ctx, p.ID)
	if len(files) != 1 || files[0].Path != "main/en.xlf" || files[0].Format != CrowdinCSV {
		t.Fatalf("files = %+v", files)
	}

	if s := pull(f(hello, bye, more)); s.Inserted != 0 || s.Updated != 0 || s.Obsolete != 0 {
		t.Errorf("identical re-pull changed things: %+v", s)
	}

	// A translation written here is not replaced by Crowdin's: bye gets a
	// human text locally, then Crowdin sends another one.
	own := "Бывай"
	if _, err := st.SaveUnit(ctx, unitByKey(t, ctx, st, p.ID, "bye").ID, store.Save{Target: &own, State: model.Translated, Origin: "edit"}, runner); err != nil {
		t.Fatal(err)
	}
	bye2 := bye
	bye2.Target, bye2.HasTarget, bye2.State = "Да пабачэння", true, model.Translated
	// more disappears from Crowdin.
	s = pull(f(hello, bye2))
	if got := unitByKey(t, ctx, st, p.ID, "bye"); model.Plain(got.Target) != "Бывай" {
		t.Errorf("human translation was overwritten: %v", got.Target)
	}
	if s.Obsolete != 1 {
		t.Errorf("removed string: %+v", s)
	}
}
