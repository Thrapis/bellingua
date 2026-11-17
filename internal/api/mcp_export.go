package api

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Thrapis/bellingua/internal/store"
)

// export_units: write the units matching a filter to a TSV file, with whole
// texts, so an agent can read a selection from disk in one go (and re-export
// to see exactly what changed) instead of paging through list_units.
//
// The file goes into an "exports" folder next to the database and nowhere
// else: the agent chooses a file name, never a location.

type exportIn struct {
	filterIn
	Path string `json:"path" jsonschema:"file name for the export, e.g. changed-by-ai.tsv: letters, digits, dot, dash, underscore, ending in .tsv or .txt. It is written into the exports folder next to the database; an existing file of that name is replaced"`
}

type mcpExport struct {
	Path  string `json:"path"` // absolute
	Rows  int    `json:"rows"`
	Bytes int64  `json:"bytes"`
}

var exportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.(tsv|txt)$`)

// reservedNames are Windows device names: a file called nul.tsv is the device.
var reservedNames = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

func validExportName(name string) error {
	if !exportName.MatchString(name) {
		return badRequest("path must be a plain file name of letters, digits, dot, dash and underscore (up to 100 characters) ending in .tsv or .txt, not %q", name)
	}
	if reservedNames.MatchString(strings.SplitN(name, ".", 2)[0]) {
		return badRequest("%q is a reserved file name", name)
	}
	return nil
}

// exportDir is the folder exports are written to.
func (s *Server) exportDir() (string, error) {
	db, err := filepath.Abs(s.cfg.DB)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(db), "exports"), nil
}

// tsvText escapes a text for one TSV field: backslash, tab, line breaks.
var tsvText = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

func (s *Server) exportUnits(ctx context.Context, in exportIn) (mcpExport, error) {
	var out mcpExport
	if err := validExportName(in.Path); err != nil {
		return out, err
	}
	if _, err := s.st.Project(ctx, in.Project); err != nil {
		return out, err
	}
	f, err := in.filter()
	if err != nil {
		return out, err
	}
	dir, err := s.exportDir()
	if err != nil {
		return out, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, err
	}
	// Written under a temporary name and renamed, so a reader never sees half a file.
	tmp, err := os.CreateTemp(dir, ".export-*.tmp")
	if err != nil {
		return out, err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	w := bufio.NewWriterSize(tmp, 1<<20)
	w.WriteString("id\tstate\tupdated\tpath\tkey\tsource\ttarget\n")
	err = s.st.ExportUnits(ctx, f, func(r store.ExportRow) error {
		target := ""
		if r.Target != nil {
			target = *r.Target
		}
		w.WriteString(strconv.FormatInt(r.ID, 10))
		w.WriteByte('\t')
		w.WriteString(r.State.String())
		w.WriteByte('\t')
		w.WriteString(time.Unix(r.UpdatedAt, 0).UTC().Format(time.RFC3339))
		for _, text := range []string{r.Path, r.Key, r.Source, target} {
			w.WriteByte('\t')
			w.WriteString(tsvText.Replace(text))
		}
		_, err := w.WriteString("\n")
		out.Rows++
		return err
	})
	if err == nil {
		err = w.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return out, err
	}
	st, err := os.Stat(tmp.Name())
	if err != nil {
		return out, err
	}
	out.Bytes = st.Size()
	out.Path = filepath.Join(dir, in.Path)
	if err := os.Rename(tmp.Name(), out.Path); err != nil {
		return out, err
	}
	return out, nil
}
