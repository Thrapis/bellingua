// Package xliff reads and writes XLIFF 1.2 files, the translation interchange
// format understood by CAT tools (OmegaT, Weblate, Crowdin, Poedit, ...).
//
// A segment is a sequence of Pieces: free text, or a Code - native markup that
// must reach the product unchanged. Codes are written as <ph id="N">markup</ph>,
// which tools show as locked tags the translator can move but not edit. Ids
// are assigned per unit from the source; a target code refers to the source
// code with the same id, so on Read a code always comes back as the exact
// source markup, whatever the tool did to the element's content.
package xliff

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Thrapis/bellingua/internal/model"
)

// Piece is a run of text or one locked code.
type Piece = model.Piece

// Unit is one translatable string.
type Unit struct {
	ID     string
	Note   string  // context shown to the translator
	Source []Piece // never empty
	Target []Piece // nil = untranslated
	State  string  // XLIFF target state, e.g. "needs-review-translation", "translated", "final"
}

// File is one XLIFF document holding one <file> element.
type File struct {
	Original   string // name of the file the units come from
	SourceLang string
	TargetLang string
	Units      []Unit
}

// Plain concatenates pieces back into the native string.
func Plain(ps []Piece) string { return model.Plain(ps) }

// Write serialises f. A unit whose target codes cannot all be matched to a
// source code of the same text is written without a target; its id is
// returned in dropped.
func Write(w io.Writer, f *File) (dropped []string, err error) {
	bw := bufio.NewWriter(w)
	bw.WriteString(xml.Header)
	bw.WriteString(`<xliff version="1.2" xmlns="urn:oasis:names:tc:xliff:document:1.2">` + "\n")
	fmt.Fprintf(bw, `  <file original="%s" source-language="%s" target-language="%s" datatype="plaintext">`+"\n",
		attr(f.Original), attr(f.SourceLang), attr(f.TargetLang))
	bw.WriteString("    <body>\n")
	for _, u := range f.Units {
		srcIDs := codeIDs(u.Source)
		tgtIDs, ok := matchCodes(u.Source, srcIDs, u.Target)
		fmt.Fprintf(bw, `      <trans-unit id="%s" xml:space="preserve">`+"\n", attr(u.ID))
		bw.WriteString("        <source>")
		writeSegment(bw, u.Source, srcIDs)
		bw.WriteString("</source>\n")
		switch {
		case u.Target == nil:
		case !ok:
			dropped = append(dropped, u.ID)
		default:
			if u.State != "" {
				fmt.Fprintf(bw, `        <target state="%s">`, attr(u.State))
			} else {
				bw.WriteString("        <target>")
			}
			writeSegment(bw, u.Target, tgtIDs)
			bw.WriteString("</target>\n")
		}
		if u.Note != "" {
			bw.WriteString("        <note>")
			xml.EscapeText(bw, []byte(u.Note))
			bw.WriteString("</note>\n")
		}
		bw.WriteString("      </trans-unit>\n")
	}
	bw.WriteString("    </body>\n  </file>\n</xliff>\n")
	return dropped, bw.Flush()
}

// codeIDs numbers the codes of a source segment 1..n (0 for text pieces).
func codeIDs(ps []Piece) []int {
	ids := make([]int, len(ps))
	n := 0
	for i, p := range ps {
		if p.Code {
			n++
			ids[i] = n
		}
	}
	return ids
}

// matchCodes gives each target code the id of an unused source code with the
// same text. ok is false if some target code has no such source code.
func matchCodes(src []Piece, srcIDs []int, tgt []Piece) (ids []int, ok bool) {
	used := make([]bool, len(src))
	ids = make([]int, len(tgt))
	for i, p := range tgt {
		if !p.Code {
			continue
		}
		found := false
		for j, s := range src {
			if s.Code && !used[j] && s.Text == p.Text {
				used[j], ids[i], found = true, srcIDs[j], true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return ids, true
}

func writeSegment(bw *bufio.Writer, ps []Piece, ids []int) {
	for i, p := range ps {
		if !p.Code {
			xml.EscapeText(bw, []byte(p.Text))
			continue
		}
		fmt.Fprintf(bw, `<ph id="%d">`, ids[i])
		xml.EscapeText(bw, []byte(p.Text))
		bw.WriteString("</ph>")
	}
}

func attr(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// --- reading -----------------------------------------------------------------

// Problem describes a unit whose target could not be used.
type Problem struct {
	ID     string
	Reason string
}

// Read parses an XLIFF 1.2 document (the first <file> only). Targets are
// rebuilt against the source codes: a target that references an unknown code
// id, repeats one, or leaves one out is dropped (Target = nil) and reported
// in problems, so broken markup never reaches the product.
func Read(r io.Reader) (*File, []Problem, error) {
	dec := xml.NewDecoder(r)
	f := &File{}
	var problems []Problem
	var cur *Unit
	var srcCodes map[string]string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("xliff: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "trans-unit" && cur != nil {
				f.Units = append(f.Units, *cur)
				cur = nil
			}
			continue
		}
		switch se.Name.Local {
		case "file":
			if f.Original == "" {
				f.Original = attrOf(se, "original")
				f.SourceLang = attrOf(se, "source-language")
				f.TargetLang = attrOf(se, "target-language")
			}
		case "trans-unit":
			cur = &Unit{ID: attrOf(se, "id")}
		case "source":
			if cur == nil {
				continue
			}
			ps, codes, err := readSegment(dec, se.Name.Local)
			if err != nil {
				return nil, nil, fmt.Errorf("xliff: unit %s source: %w", cur.ID, err)
			}
			srcCodes = map[string]string{}
			for _, c := range codes {
				srcCodes[c.id] = c.text
			}
			cur.Source = ps
		case "target":
			if cur == nil {
				continue
			}
			cur.State = attrOf(se, "state")
			ps, codes, err := readSegment(dec, se.Name.Local)
			if err != nil {
				return nil, nil, fmt.Errorf("xliff: unit %s target: %w", cur.ID, err)
			}
			if reason := resolveCodes(ps, codes, srcCodes); reason != "" {
				problems = append(problems, Problem{cur.ID, reason})
				continue
			}
			cur.Target = ps
		case "note":
			if cur == nil {
				continue
			}
			var s string
			if err := dec.DecodeElement(&s, &se); err != nil {
				return nil, nil, fmt.Errorf("xliff: unit %s note: %w", cur.ID, err)
			}
			cur.Note = s
		}
	}
	return f, problems, nil
}

type codeRef struct {
	piece int // index into the segment's pieces
	id    string
	text  string
}

// readSegment reads the mixed content of <source>/<target> up to its end tag.
// <ph>/<x>/<bx>/<ex>/<it> become Code pieces keyed by their id.
func readSegment(dec *xml.Decoder, end string) ([]Piece, []codeRef, error) {
	var ps []Piece
	var codes []codeRef
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			if n := len(ps); n > 0 && !ps[n-1].Code {
				ps[n-1].Text += string(t)
			} else {
				ps = append(ps, Piece{Text: string(t)})
			}
		case xml.StartElement:
			var inner struct {
				Text string `xml:",chardata"`
			}
			if err := dec.DecodeElement(&inner, &t); err != nil {
				return nil, nil, err
			}
			codes = append(codes, codeRef{piece: len(ps), id: attrOf(t, "id"), text: inner.Text})
			ps = append(ps, Piece{Text: inner.Text, Code: true})
		case xml.EndElement:
			if t.Name.Local == end {
				return ps, codes, nil
			}
		}
	}
}

// resolveCodes replaces each target code's text with the source code of the
// same id and checks that every source code is used exactly once.
func resolveCodes(ps []Piece, codes []codeRef, src map[string]string) string {
	seen := map[string]bool{}
	for _, c := range codes {
		native, ok := src[c.id]
		switch {
		case !ok:
			return fmt.Sprintf("unknown tag id %q", c.id)
		case seen[c.id]:
			return fmt.Sprintf("tag id %q used twice", c.id)
		}
		seen[c.id] = true
		ps[c.piece].Text = native
	}
	if len(seen) != len(src) {
		var missing []string
		for id := range src {
			if !seen[id] {
				missing = append(missing, id)
			}
		}
		return "missing tag ids " + strings.Join(sortedNums(missing), ",")
	}
	return ""
}

func sortedNums(ids []string) []string {
	sort.Slice(ids, func(i, j int) bool {
		x, e1 := strconv.Atoi(ids[i])
		y, e2 := strconv.Atoi(ids[j])
		if e1 == nil && e2 == nil {
			return x < y
		}
		return ids[i] < ids[j]
	})
	return ids
}

func attrOf(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
