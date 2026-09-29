package glossary

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Thrapis/bellingua/internal/model"
)

// Pin prepares a source for machine translation with the glossary: every
// term occurrence that has an agreed translation (or is DNT) becomes a code
// piece holding a pin token, so the MT layer masks it like markup and the
// model never sees the word. Unpin swaps the tokens in the translation for
// the agreed target terms. repl[i] is the text for pin i; none means there
// is nothing to pin.
//
// The term's translation for the matching form is inserted (see Term.Forms),
// else its base form, so inflected positions without a form may need a
// manual fix; that is the price of a guaranteed term.
func (g *Glossary) Pin(src []model.Piece) (pinned []model.Piece, repl []string) {
	spans := g.spans(src)
	if len(spans) == 0 {
		return src, nil
	}
	off := 0
	k := 0
	for _, p := range src {
		end := off + len(p.Text)
		if p.Code {
			pinned = append(pinned, p)
			off = end
			continue
		}
		last := off // start of the not yet emitted text of p
		for ; k < len(spans) && spans[k].start < end; k++ {
			sp := spans[k]
			if sp.start < last || sp.end > end {
				continue // crosses a code piece: leave it to the model
			}
			r, ok := g.targetFor(sp.term, p.Text[sp.start-off:sp.end-off])
			if !ok {
				continue
			}
			if sp.start > last {
				pinned = append(pinned, model.Piece{Text: p.Text[last-off : sp.start-off]})
			}
			pinned = append(pinned, model.Piece{Text: pinToken(len(repl)), Code: true})
			repl = append(repl, r)
			last = sp.end
		}
		if last < end {
			pinned = append(pinned, model.Piece{Text: p.Text[last-off:]})
		}
		off = end
	}
	if len(repl) == 0 {
		return src, nil
	}
	return pinned, repl
}

// Unpin replaces the pin tokens in a translation of Pin's output with the
// agreed terms. Pins come back inside code pieces, possibly merged with
// adjacent markup, so each code piece is split around its tokens.
func Unpin(ps []model.Piece, repl []string) []model.Piece {
	var out []model.Piece
	add := func(p model.Piece) {
		if p.Text == "" {
			return
		}
		if n := len(out); n > 0 && !p.Code && !out[n-1].Code {
			out[n-1].Text += p.Text
			return
		}
		out = append(out, p)
	}
	for _, p := range ps {
		if !p.Code {
			add(p)
			continue
		}
		last := 0
		for _, loc := range pinRe.FindAllStringSubmatchIndex(p.Text, -1) {
			add(model.Piece{Text: p.Text[last:loc[0]], Code: true})
			if i, err := strconv.Atoi(p.Text[loc[2]:loc[3]]); err == nil && i < len(repl) {
				add(model.Piece{Text: repl[i]})
			}
			last = loc[1]
		}
		add(model.Piece{Text: p.Text[last:], Code: true})
	}
	if out == nil {
		out = []model.Piece{}
	}
	return out
}

// Pin tokens use private-use characters: they only ever live inside code
// pieces, which are never sent to a provider.
func pinToken(i int) string { return "" + strconv.Itoa(i) + "" }

var pinRe = regexp.MustCompile("(\\d+)")

// matchCase gives the target term the capitalisation of the source
// occurrence: all caps stay all caps, a capitalised word (say, at a sentence
// start) capitalises the term. A capitalised term (a name) is left as is.
func matchCase(occ, target string) string {
	letters, upper := 0, 0
	for _, r := range occ {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters > 1 && upper == letters {
		return strings.ToUpper(target)
	}
	if startsUpper(occ) && !startsUpper(target) {
		r, n := utf8.DecodeRuneInString(target)
		return string(unicode.ToUpper(r)) + target[n:]
	}
	return target
}
