package glossary

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// FormCount is an inflected form of a term found in the texts, with how
// many times it occurs.
type FormCount struct {
	Form  string `json:"form"`
	Count int    `json:"count"`
}

// FormCounter collects the forms of one term across many source strings,
// for filling in Term.Forms.
//
// It matches more loosely than the glossary, because the stemmer does not
// always give a name's forms one stem: a word is a form of a term word when
// it starts with the longer of the word's stem and its prefixOf, and is at
// most 3 letters longer than the term word. The user reviews the result.
type FormCounter struct {
	keys   []looseKey
	base   string                    // normPhrase of the term itself
	counts map[string]map[string]int // normalised form -> spelling -> count
}

type looseKey struct {
	prefix string
	maxLen int // in runes
}

// NewFormCounter counts forms of source in sourceLang, in any capitalisation.
func NewFormCounter(sourceLang, source string) *FormCounter {
	stem := stemmerFor(sourceLang)
	c := &FormCounter{base: normPhrase(source), counts: map[string]map[string]int{}}
	for _, w := range tokenize(source, 0) {
		n := norm(w.text)
		p := stem(w.text)
		if q := prefixOf(n); !strings.HasPrefix(n, p) || utf8.RuneCountInString(q) > utf8.RuneCountInString(p) {
			p = q
		}
		c.keys = append(c.keys, looseKey{p, utf8.RuneCountInString(n) + 3})
	}
	return c
}

// Add counts the forms in one source string.
func (c *FormCounter) Add(src string) {
	if len(c.keys) == 0 {
		return
	}
	words := tokenize(src, 0)
next:
	for i := 0; i+len(c.keys) <= len(words); i++ {
		for k, key := range c.keys {
			n := norm(words[i+k].text)
			if !strings.HasPrefix(n, key.prefix) || utf8.RuneCountInString(n) > key.maxLen {
				continue next
			}
		}
		occ := src[words[i].start:words[i+len(c.keys)-1].end]
		i += len(c.keys) - 1
		k := normPhrase(occ)
		if k == c.base {
			continue // the base form is the term's own translation
		}
		if c.counts[k] == nil {
			c.counts[k] = map[string]int{}
		}
		c.counts[k][occ]++
	}
}

// Result lists the forms, most frequent first, each in its most frequent
// spelling.
func (c *FormCounter) Result() []FormCount {
	out := []FormCount{}
	for _, spellings := range c.counts {
		var best FormCount
		total := 0
		for sp, n := range spellings {
			total += n
			if n > best.Count || n == best.Count && sp < best.Form {
				best = FormCount{sp, n}
			}
		}
		out = append(out, FormCount{best.Form, total})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Form < out[j].Form
	})
	return out
}

// SearchKey is a lower-case substring that every form of source (as
// FormCounter sees it) contains, for a text index to find candidate strings.
// "" when source has no word.
func SearchKey(sourceLang, source string) string {
	if c := NewFormCounter(sourceLang, source); len(c.keys) > 0 {
		return c.keys[0].prefix
	}
	return ""
}
