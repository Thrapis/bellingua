// Package glossary finds a project's terms in source strings and checks that
// translations use the agreed target terms.
//
// Source matching is morphological for Russian: every word is reduced to its
// Snowball stem, so the term «Орбита» also matches «Орбиты», «Орбитой».
// Other source languages match on the lower-cased word. Multi-word terms
// match consecutive words. Markup (code pieces) separates words but never
// takes part in a match.
//
// Target checking cannot stem (only source words are stemmed), so a target
// word counts as present when some word of the translation starts with the
// term word minus a short inflectional tail (see prefixOf).
package glossary

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/kljensen/snowball/russian"

	"github.com/Thrapis/bellingua/internal/model"
)

// Term is one glossary entry.
type Term struct {
	ID            int64  `json:"id"`
	Source        string `json:"source"`
	Target        string `json:"target"` // alternatives separated by "|"
	Note          string `json:"note"`
	DNT           bool   `json:"dnt"`           // do not translate: must appear unchanged in the target
	CaseSensitive bool   `json:"caseSensitive"` // match only with the same capitalisation
	Forms         []Form `json:"forms"`         // inflected source forms with their translations
	UpdatedAt     int64  `json:"updatedAt"`
}

// Form is one inflected form of a term («Баумана») and its translation
// («Баўмана»). Target is the base form, used where no form matches.
type Form struct {
	Src string `json:"src"`
	Tgt string `json:"tgt"`
}

// Targets returns the non-empty target alternatives.
func (t Term) Targets() []string {
	var out []string
	for _, v := range strings.Split(t.Target, "|") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Match is one occurrence of a term in a source string. Start and End are
// UTF-16 offsets into the plain string (what JavaScript indexes by). Insert
// is the agreed translation for this very form ("" when the term has none).
type Match struct {
	TermID int64  `json:"term"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Insert string `json:"insert"`
}

// Glossary is an immutable, concurrency-safe matcher over a set of terms.
type Glossary struct {
	stem  func(string) string
	terms []Term
	first map[string][]entry  // stem of a term's first word -> entries
	forms []map[string]string // per term: norm(form source) -> form translation
}

type entry struct {
	term  int // index into terms
	stems []string
	upper []bool // capitalisation of each word, for case-sensitive terms
}

// New builds a matcher for terms in sourceLang.
func New(sourceLang string, terms []Term) *Glossary {
	g := &Glossary{stem: stemmerFor(sourceLang), terms: terms, first: map[string][]entry{}, forms: make([]map[string]string, len(terms))}
	for i, t := range terms {
		words := tokenize(t.Source, 0)
		if len(words) == 0 {
			continue
		}
		e := entry{term: i}
		for _, w := range words {
			e.stems = append(e.stems, g.stem(w.text))
			e.upper = append(e.upper, startsUpper(w.text))
		}
		g.first[e.stems[0]] = append(g.first[e.stems[0]], e)

		// Every form also matches on its own: the stemmer does not always
		// reduce a name's forms to one stem («Бауман», «Баумана» → «баума»,
		// but «Бауману» → «бауман»). Capitalisation stays the term's.
		seen := map[string]bool{strings.Join(e.stems, " "): true}
		for _, f := range t.Forms {
			if g.forms[i] == nil {
				g.forms[i] = map[string]string{}
			}
			g.forms[i][normPhrase(f.Src)] = f.Tgt
			fe := entry{term: i}
			for k, w := range tokenize(f.Src, 0) {
				fe.stems = append(fe.stems, g.stem(w.text))
				if len(e.upper) > k {
					fe.upper = append(fe.upper, e.upper[k])
				} else {
					fe.upper = append(fe.upper, startsUpper(w.text))
				}
			}
			if key := strings.Join(fe.stems, " "); len(fe.stems) > 0 && !seen[key] {
				seen[key] = true
				g.first[fe.stems[0]] = append(g.first[fe.stems[0]], fe)
			}
		}
	}
	// Longer terms first, so "Орбита Корпорейшн" wins over "Орбита".
	for k, es := range g.first {
		for i := 1; i < len(es); i++ {
			for j := i; j > 0 && len(es[j].stems) > len(es[j-1].stems); j-- {
				es[j], es[j-1] = es[j-1], es[j]
			}
		}
		g.first[k] = es
	}
	return g
}

// Len is the number of terms.
func (g *Glossary) Len() int {
	if g == nil {
		return 0
	}
	return len(g.terms)
}

// Term returns a term by ID.
func (g *Glossary) Term(id int64) (Term, bool) {
	for _, t := range g.terms {
		if t.ID == id {
			return t, true
		}
	}
	return Term{}, false
}

type word struct {
	text       string
	start, end int // byte offsets in the plain string
}

// tokenize splits s into words: runs of letters/digits, joined by an inner
// hyphen or apostrophe («кибер-психоз», «камп'ютар»). base is added to offsets.
func tokenize(s string, base int) []word {
	var out []word
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isWordRune(r) {
			i += n
			continue
		}
		start := i
		i += n
		for i < len(s) {
			r, n := utf8.DecodeRuneInString(s[i:])
			if isWordRune(r) {
				i += n
				continue
			}
			if isJoiner(r) && i+n < len(s) {
				if r2, _ := utf8.DecodeRuneInString(s[i+n:]); isWordRune(r2) {
					i += n
					continue
				}
			}
			break
		}
		out = append(out, word{s[start:i], base + start, base + i})
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
func isJoiner(r rune) bool   { return r == '-' || r == '\'' || r == '’' || r == 'ʼ' }

func startsUpper(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// norm lower-cases and folds letters that spelling treats as variants.
func norm(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("ё", "е", "ў", "у", "’", "'", "ʼ", "'").Replace(s)
}

// stemCache memoises stems across all glossaries: a large project's vocabulary is a
// few hundred thousand words, and QA rechecks stem every word of every unit.
var stemCache sync.Map

func stemmerFor(lang string) func(string) string {
	if lang != "ru" {
		return norm
	}
	return func(w string) string {
		if v, ok := stemCache.Load(w); ok {
			return v.(string)
		}
		s := russian.Stem(norm(w), true)
		stemCache.Store(w, s)
		return s
	}
}

// Match finds term occurrences in a source given as pieces. Matches never
// overlap; at each word the longest term wins.
func (g *Glossary) Match(src []model.Piece) []Match {
	plain := model.Plain(src)
	var out []Match
	for _, sp := range g.spans(src) {
		ins, _ := g.targetFor(sp.term, plain[sp.start:sp.end])
		out = append(out, Match{TermID: g.terms[sp.term].ID, Start: utf16Len(plain[:sp.start]), End: utf16Len(plain[:sp.end]), Insert: ins})
	}
	return out
}

// targetFor is the translation to insert for an occurrence occ of term i:
// the occurrence itself for DNT, the translation of the matching form, or
// the base translation; with occ's capitalisation. ok is false when the term
// has no translation.
func (g *Glossary) targetFor(i int, occ string) (string, bool) {
	t := g.terms[i]
	if t.DNT {
		return occ, true
	}
	if v, ok := g.forms[i][normPhrase(occ)]; ok && v != "" {
		return matchCase(occ, v), true
	}
	if ts := t.Targets(); len(ts) > 0 {
		return matchCase(occ, ts[0]), true
	}
	return "", false
}

// normPhrase normalises a (multi-word) form for lookup: norm of each word,
// single-spaced, so markup-free whitespace differences don't matter.
func normPhrase(s string) string {
	var ws []string
	for _, w := range tokenize(s, 0) {
		ws = append(ws, norm(w.text))
	}
	return strings.Join(ws, " ")
}

// span is a match as byte offsets into the plain string.
type span struct{ term, start, end int }

func (g *Glossary) spans(src []model.Piece) []span {
	if g.Len() == 0 {
		return nil
	}
	var words []word
	off := 0
	for _, p := range src {
		if !p.Code {
			words = append(words, tokenize(p.Text, off)...)
		}
		off += len(p.Text)
	}
	stems := make([]string, len(words))
	for i, w := range words {
		stems[i] = g.stem(w.text)
	}

	var out []span
	for i := 0; i < len(words); {
		matched := 0
		for _, e := range g.first[stems[i]] {
			if g.matchAt(e, words, stems, i) {
				out = append(out, span{e.term, words[i].start, words[i+len(e.stems)-1].end})
				matched = len(e.stems)
				break
			}
		}
		if matched > 0 {
			i += matched
		} else {
			i++
		}
	}
	return out
}

func (g *Glossary) matchAt(e entry, words []word, stems []string, i int) bool {
	if i+len(e.stems) > len(words) {
		return false
	}
	cs := g.terms[e.term].CaseSensitive
	for k, st := range e.stems {
		if stems[i+k] != st {
			return false
		}
		if cs && startsUpper(words[i+k].text) != e.upper[k] {
			return false
		}
	}
	return true
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// Problem is a term the translation does not use as agreed.
type Problem struct {
	Term Term
}

// Check returns the terms found in src whose agreed translation is missing
// from tgt. Terms without a target (and not DNT) are informational only.
func (g *Glossary) Check(src, tgt []model.Piece) []Problem {
	matches := g.Match(src)
	if len(matches) == 0 {
		return nil
	}
	var tgtWords []string
	tgtText := ""
	for _, p := range tgt {
		if !p.Code {
			tgtText += p.Text + "⁠"
		}
	}
	for _, w := range tokenize(tgtText, 0) {
		tgtWords = append(tgtWords, norm(w.text))
	}
	normTgt := norm(tgtText)

	var out []Problem
	seen := map[int64]bool{}
	for _, m := range matches {
		if seen[m.TermID] {
			continue
		}
		seen[m.TermID] = true
		t, _ := g.Term(m.TermID)
		switch {
		case t.DNT:
			if !strings.Contains(normTgt, norm(t.Source)) {
				out = append(out, Problem{t})
			}
		case len(t.Targets()) > 0 || len(t.Forms) > 0:
			ok := false
			cands := t.Targets()
			for _, f := range t.Forms {
				cands = append(cands, f.Tgt)
			}
			for _, v := range cands {
				if containsPhrase(tgtWords, v) {
					ok = true
					break
				}
			}
			if !ok {
				out = append(out, Problem{t})
			}
		}
	}
	return out
}

// containsPhrase reports whether every word of phrase appears (by prefix,
// see prefixOf) as consecutive words of text.
func containsPhrase(text []string, phrase string) bool {
	var ps []string
	for _, w := range tokenize(phrase, 0) {
		ps = append(ps, prefixOf(norm(w.text)))
	}
	if len(ps) == 0 {
		return true
	}
outer:
	for i := 0; i+len(ps) <= len(text); i++ {
		for k, p := range ps {
			if !strings.HasPrefix(text[i+k], p) {
				continue outer
			}
		}
		return true
	}
	return false
}

// prefixOf drops a short inflectional tail so a target term matches its
// inflected forms: words of 5+ letters lose up to two letters (keeping at
// least four), shorter words must match whole.
func prefixOf(w string) string {
	rs := []rune(w)
	switch n := len(rs); {
	case n <= 4:
		return w
	case n == 5:
		return string(rs[:4])
	default:
		return string(rs[:n-2])
	}
}
