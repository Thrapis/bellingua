// Package model holds the types shared by the store, formats, QA and API.
package model

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Piece is a run of text or one locked code: native markup that must reach
// the product unchanged. Translators may move codes but never edit them.
type Piece struct {
	Text string `json:"t"`
	Code bool   `json:"c,omitempty"`
}

// Plain concatenates pieces back into the native string.
func Plain(ps []Piece) string {
	if len(ps) == 1 {
		return ps[0].Text
	}
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.Text)
	}
	return b.String()
}

// HasCodes reports whether any piece is a code.
func HasCodes(ps []Piece) bool {
	for _, p := range ps {
		if p.Code {
			return true
		}
	}
	return false
}

// Codes returns the code texts of ps in order.
func Codes(ps []Piece) []string {
	var out []string
	for _, p := range ps {
		if p.Code {
			out = append(out, p.Text)
		}
	}
	return out
}

// EncodePieces packs pieces into a compact blob: per piece one kind byte
// (0 text, 1 code), a uvarint length and the bytes. Pieces without any code
// encode to nil — the plain text column is then the whole story.
func EncodePieces(ps []Piece) []byte {
	if !HasCodes(ps) {
		return nil
	}
	n := 0
	for _, p := range ps {
		n += 1 + binary.MaxVarintLen32 + len(p.Text)
	}
	b := make([]byte, 0, n)
	for _, p := range ps {
		if p.Code {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
		b = binary.AppendUvarint(b, uint64(len(p.Text)))
		b = append(b, p.Text...)
	}
	return b
}

var errBadBlob = errors.New("model: corrupt pieces blob")

// DecodePieces is the inverse of EncodePieces. A nil blob yields the plain
// text as a single text piece (or no pieces when plain is empty).
func DecodePieces(blob []byte, plain string) ([]Piece, error) {
	if blob == nil {
		if plain == "" {
			return []Piece{}, nil
		}
		return []Piece{{Text: plain}}, nil
	}
	var ps []Piece
	for len(blob) > 0 {
		kind := blob[0]
		l, n := binary.Uvarint(blob[1:])
		if n <= 0 || uint64(len(blob)-1-n) < l {
			return nil, errBadBlob
		}
		start := 1 + n
		ps = append(ps, Piece{Text: string(blob[start : start+int(l)]), Code: kind == 1})
		blob = blob[start+int(l):]
	}
	return ps, nil
}

// State is a unit's workflow state. The order matters: higher is further
// along, and filters such as "at least translated" compare numerically.
type State int

const (
	Untranslated State = iota
	MT                 // machine translation, not yet reviewed
	Translated         // written or edited by a human
	Approved           // reviewed and final
)

var stateNames = [...]string{"untranslated", "mt", "translated", "approved"}

func (s State) String() string {
	if s >= 0 && int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// ParseState maps a name back to a State.
func ParseState(s string) (State, bool) {
	for i, n := range stateNames {
		if n == s {
			return State(i), true
		}
	}
	return 0, false
}

// MarshalText makes State serialise as its name in JSON.
func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *State) UnmarshalText(b []byte) error {
	v, ok := ParseState(string(b))
	if !ok {
		return errors.New("model: unknown state " + string(b))
	}
	*s = v
	return nil
}

// XLIFFState maps a State to the XLIFF 1.2 target state attribute.
func (s State) XLIFFState() string {
	switch s {
	case MT:
		return "needs-review-translation"
	case Translated:
		return "translated"
	case Approved:
		return "final"
	}
	return ""
}

// FromXLIFFState maps an XLIFF target state to a State (for a unit that has a
// target). Unknown or empty states count as machine output awaiting review.
func FromXLIFFState(s string) State {
	switch s {
	case "translated", "needs-review-l10n", "needs-l10n", "needs-adaptation", "needs-review-adaptation":
		return Translated
	case "final", "signed-off":
		return Approved
	}
	return MT
}
