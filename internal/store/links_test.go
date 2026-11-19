package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectLinks(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, _ := st.CreateProject(ctx, "drg", "en", "be")

	if _, err := st.Link(ctx, p.ID, LinkCrowdin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked project: err = %v", err)
	}
	l := Link{ProjectID: p.ID, Kind: LinkCrowdin, RemoteID: 42, Identifier: "DRG-Core", SourceLang: "en", TargetLang: "be"}
	if err := st.SetLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	got, err := st.Link(ctx, p.ID, LinkCrowdin)
	if err != nil || got.RemoteID != 42 || got.Identifier != "drg-core" || got.LastPull != 0 {
		t.Fatalf("link = %+v, %v (identifier is stored lower case)", got, err)
	}
	// Linking again replaces the link but keeps when it was last pulled.
	if err := st.MarkPulled(ctx, p.ID, LinkCrowdin, 1234); err != nil {
		t.Fatal(err)
	}
	l.RemoteID = 43
	if err := st.SetLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.Link(ctx, p.ID, LinkCrowdin); got.RemoteID != 43 || got.LastPull != 1234 {
		t.Errorf("relink = %+v", got)
	}
	// An editor URL names the project by identifier, in any case.
	byID, err := st.LinkByIdentifier(ctx, LinkCrowdin, "DRG-CORE")
	if err != nil || byID.ProjectID != p.ID {
		t.Errorf("by identifier = %+v, %v", byID, err)
	}
	if _, err := st.LinkByIdentifier(ctx, LinkCrowdin, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown identifier: %v", err)
	}
	if err := st.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Link(ctx, p.ID, LinkCrowdin); !errors.Is(err, ErrNotFound) {
		t.Errorf("link survived its project: %v", err)
	}
}
