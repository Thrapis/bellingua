package api

import (
	"strings"
	"testing"

	"github.com/Thrapis/bellingua/internal/store"
)

func testFiles() []*store.File {
	var out []*store.File
	for i, p := range []string{
		"base/Quest/Main.xlf",
		"base/Quest/Side.xlf",
		"ep1/Docs/Readme.xlf",
		"ep1/ку/файл.xlf",
	} {
		dir := p[:strings.LastIndexByte(p, '/')]
		out = append(out, &store.File{ID: int64(i + 1), Path: p, Dir: dir})
	}
	return out
}

func paths(ns []TreeNode) string {
	var s []string
	for _, n := range ns {
		s = append(s, n.Path)
	}
	return strings.Join(s, ",")
}

// Search finds files and folders whose path holds every word, ignoring case
// (Cyrillic too); folders come first.
func TestTreeSearch(t *testing.T) {
	c := buildTree(testFiles())
	for _, tc := range []struct{ q, want string }{
		{"quest", "base/Quest,base/Quest/Main.xlf,base/Quest/Side.xlf"},
		{"main QUEST", "base/Quest/Main.xlf"},
		{"  side  ", "base/Quest/Side.xlf"},
		{"ФАЙЛ", "ep1/ку/файл.xlf"},
		{"ep1/ку", "ep1/ку,ep1/ку/файл.xlf"},
		{"nothing", ""},
		{"", ""},
	} {
		got, total := c.search(tc.q, 100)
		if paths(got) != tc.want || total != len(got) {
			t.Errorf("search(%q) = %q (total %d), want %q", tc.q, paths(got), total, tc.want)
		}
	}

	got, total := c.search("xlf", 2)
	if len(got) != 2 || total != 4 {
		t.Errorf("limit: got %d of %d, want 2 of 4", len(got), total)
	}
	if f := got[0]; f.FileID == 0 {
		t.Errorf("files only here, got %+v", f)
	}
}
