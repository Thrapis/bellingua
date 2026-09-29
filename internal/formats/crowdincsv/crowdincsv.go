// Package crowdincsv reads and writes the four-column CSV Crowdin imports and
// exports as-is: "id,source,translation,context" with a header line.
package crowdincsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Header is the first line of every file.
var Header = []string{"id", "source", "translation", "context"}

// Row is one translatable string.
type Row struct {
	ID, Source, Translation, Context string
}

// Read parses a file, header included.
func Read(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("crowdin csv: empty file, want header %v", Header)
	}
	if err != nil {
		return nil, fmt.Errorf("crowdin csv: %w", err)
	}
	if len(head) > 0 {
		head[0] = strings.TrimPrefix(head[0], "\xEF\xBB\xBF")
	}
	if strings.Join(head, ",") != strings.Join(Header, ",") {
		return nil, fmt.Errorf("crowdin csv: header %v, want %v", head, Header)
	}
	var rows []Row
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("crowdin csv: %w", err)
		}
		if len(rec) != len(Header) {
			return nil, fmt.Errorf("crowdin csv: line %d: %d fields, want %d", line, len(rec), len(Header))
		}
		rows = append(rows, Row{rec[0], rec[1], rec[2], rec[3]})
	}
}

// Write serialises rows with the header, LF line ends and RFC-4180 quoting.
func Write(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{r.ID, r.Source, r.Translation, r.Context}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
