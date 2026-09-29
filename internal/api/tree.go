package api

import (
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/Thrapis/bellingua/internal/store"
)

// TreeNode is a folder or file with rolled-up counters.
type TreeNode struct {
	Name   string       `json:"name"`
	Path   string       `json:"path"`
	FileID int64        `json:"file,omitempty"` // 0 for folders
	Counts store.Counts `json:"counts"`
}

// treeCache holds a project's folder aggregation for one store revision.
type treeCache struct {
	rev      int64
	children map[string][]TreeNode // folder path -> immediate children (folders first)
}

func (s *Server) treeFor(r *http.Request, projectID int64) (*treeCache, error) {
	rev := s.st.Rev()
	s.treeMu.Lock()
	c := s.trees[projectID]
	s.treeMu.Unlock()
	if c != nil && c.rev == rev {
		return c, nil
	}
	files, err := s.st.Files(r.Context(), projectID)
	if err != nil {
		return nil, err
	}
	c = buildTree(files)
	c.rev = rev
	s.treeMu.Lock()
	s.trees[projectID] = c
	s.treeMu.Unlock()
	return c, nil
}

func buildTree(files []*store.File) *treeCache {
	dirs := map[string]*TreeNode{}
	children := map[string][]TreeNode{}
	var ensure func(dir string) *TreeNode
	ensure = func(dir string) *TreeNode {
		if n, ok := dirs[dir]; ok {
			return n
		}
		n := &TreeNode{Name: path.Base(dir), Path: dir}
		dirs[dir] = n
		if dir != "" {
			ensure(parentDir(dir))
		}
		return n
	}
	ensure("")
	for _, f := range files {
		ensure(f.Dir)
		for d := f.Dir; ; d = parentDir(d) {
			dirs[d].Counts.Add(f.Counts)
			if d == "" {
				break
			}
		}
		children[f.Dir] = append(children[f.Dir], TreeNode{Name: path.Base(f.Path), Path: f.Path, FileID: f.ID, Counts: f.Counts})
	}
	// Folders go first, sorted by name; files keep path order.
	sub := map[string][]TreeNode{}
	for p, n := range dirs {
		if p != "" {
			sub[parentDir(p)] = append(sub[parentDir(p)], *n)
		}
	}
	out := map[string][]TreeNode{}
	for p := range dirs {
		ds := sub[p]
		sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
		out[p] = append(ds, children[p]...)
	}
	return &treeCache{children: out}
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// tree returns a folder's immediate children plus the folder's own totals.
// Folders are expanded lazily by the UI, one request per level.
func (s *Server) tree(w http.ResponseWriter, r *http.Request) error {
	if etag(w, r, s.st.Rev()) {
		return nil
	}
	p, err := s.project(r)
	if err != nil {
		return err
	}
	c, err := s.treeFor(r, p.ID)
	if err != nil {
		return err
	}
	dir := strings.Trim(r.URL.Query().Get("dir"), "/")
	kids, found := c.children[dir]
	if !found {
		return store.ErrNotFound
	}
	if kids == nil {
		kids = []TreeNode{}
	}
	return ok(w, map[string]any{"dir": dir, "children": kids})
}
