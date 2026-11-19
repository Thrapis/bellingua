package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Patch changes keys of the config file at path and keeps everything else:
// comments, other keys and their order. Keys are dotted paths ("backup.dir");
// a value is a string, an int, a bool or a []string. A nil value, an empty
// string or an empty slice removes the key (and a parent left empty), so the
// default applies again. The file is created when it does not exist.
//
// The result is written to a temporary file and loaded first: a change that
// would make the configuration invalid is refused and the file is untouched.
func Patch(path string, edits map[string]any) error {
	var doc yaml.Node
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	// A file with no content or only comments has no mapping yet.
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		// Keep leading comments: they are not part of any node when there is no content.
		if c := leadingComments(b); c != "" {
			doc.Content[0].HeadComment = c
		}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level is not a mapping", path)
	}
	keys := make([]string, 0, len(edits))
	for k := range edits {
		keys = append(keys, k)
	}
	// Deterministic order, so new keys land in the same places every time.
	sort.Strings(keys)
	for _, k := range keys {
		if err := setPath(root, strings.Split(k, "."), edits[k]); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	enc.Close()

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := Load(tmp.Name(), false); err != nil {
		return fmt.Errorf("not saved, the configuration would be invalid: %w", unwrapPath(err, tmp.Name()))
	}
	return os.Rename(tmp.Name(), path)
}

// unwrapPath hides the temporary file name from a Load error.
func unwrapPath(err error, tmp string) error {
	return errors.New(strings.ReplaceAll(err.Error(), tmp, "bellingua.yaml"))
}

// leadingComments returns the comment lines at the top of a file that has no
// content, as a head comment.
func leadingComments(b []byte) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	}
	return false
}

func valueNode(v any) (*yaml.Node, error) {
	switch x := v.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: x}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(x)}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(x)}, nil
	case []string:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, s := range x {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
		}
		return n, nil
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}

// find returns the index of key's value node in a mapping, or -1.
func find(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

// setPath sets or removes the key at a path under mapping m. It reports
// through its return whether m is empty afterwards only via removal below.
func setPath(m *yaml.Node, path []string, v any) error {
	i := find(m, path[0])
	if len(path) > 1 {
		if i < 0 {
			if isEmpty(v) {
				return nil
			}
			m.Content = append(m.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]},
				&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
			i = len(m.Content) - 1
		}
		child := m.Content[i]
		if child.Kind != yaml.MappingNode {
			if child.Kind == yaml.ScalarNode && child.Tag == "!!null" {
				*child = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			} else {
				return fmt.Errorf("%q is not a section", path[0])
			}
		}
		if err := setPath(child, path[1:], v); err != nil {
			return err
		}
		if len(child.Content) == 0 {
			m.Content = append(m.Content[:i-1], m.Content[i+1:]...)
		}
		return nil
	}
	if isEmpty(v) {
		if i >= 0 {
			m.Content = append(m.Content[:i-1], m.Content[i+1:]...)
		}
		return nil
	}
	n, err := valueNode(v)
	if err != nil {
		return err
	}
	if i >= 0 {
		// Keep the comment that sits on the line of the old value.
		n.LineComment = m.Content[i].LineComment
		m.Content[i] = n
		return nil
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, n)
	return nil
}
