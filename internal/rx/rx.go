// Package rx is Go's RE2 regexp adjusted for translation texts: \b, \B, \w
// and \W follow Unicode (Go's are ASCII-only, so they never work on
// Cyrillic), and an apostrophe between two letters is part of the word.
//
// RE2 has no look-around, so \b cannot be rewritten into the pattern. Each
// \b becomes an empty capture group; after the engine finds a match, the
// positions of those groups are checked and a match with a wrong one is
// dropped. The engine does not then try another way to match at the same
// place, which only matters for patterns where a lazy quantifier decides
// where a \b falls.
package rx

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const wordClass = `\p{L}\p{N}\p{M}_`

// Regexp is a compiled pattern. It is safe for concurrent use.
type Regexp struct {
	re     *regexp.Regexp
	bounds []bound
	// user[i] is the engine's group number of the caller's group i; nil when
	// no \b was added and the numbers are the same.
	user []int
	// shadow has the caller's groups only (same numbers and names); it
	// expands replacement templates.
	shadow *regexp.Regexp
	lit    string
}

// bound is an added group whose position must (or, for \B, must not) be a word boundary.
type bound struct {
	group int
	not   bool
}

// Compile parses a pattern (RE2 syntax, case-sensitive unless it says (?i)).
func Compile(pat string) (*Regexp, error) {
	if pat == "" {
		return nil, errors.New("empty pattern")
	}
	expr, plain, bounds, user := rewrite(pat)
	re, err := regexp.Compile(expr)
	if err != nil {
		// Report the caller's pattern, not the rewritten one.
		if _, perr := regexp.Compile(plain); perr != nil {
			err = perr
		}
		return nil, err
	}
	r := &Regexp{re: re, shadow: re}
	if len(bounds) > 0 {
		r.bounds, r.user = bounds, user
		if r.shadow, err = regexp.Compile(plain); err != nil {
			return nil, err
		}
	}
	if tree, err := syntax.Parse(plain, syntax.Perl); err == nil {
		r.lit = literal(tree)
	}
	return r, nil
}

// rewrite returns the pattern for the engine (expr), the same without the
// boundary groups (plain), the added groups and the engine numbers of the
// caller's groups.
func rewrite(pat string) (expr, plain string, bounds []bound, user []int) {
	var e, p strings.Builder
	both := func(s string) { e.WriteString(s); p.WriteString(s) }
	user = []int{0}
	groups := 0
	inClass := false
	for i := 0; i < len(pat); {
		c := pat[i]
		switch {
		case c == '\\' && i+1 < len(pat):
			n := pat[i+1]
			switch {
			case n == 'Q': // quoted text runs to \E
				end := strings.Index(pat[i:], `\E`)
				if end < 0 {
					both(pat[i:])
					i = len(pat)
					continue
				}
				both(pat[i : i+end+2])
				i += end + 2
				continue
			case (n == 'b' || n == 'B') && !inClass:
				groups++
				bounds = append(bounds, bound{groups, n == 'B'})
				e.WriteString("()")
				p.WriteString("(?:)")
			case n == 'w' && inClass:
				both(wordClass)
			case n == 'w':
				both("[" + wordClass + "]")
			case n == 'W' && !inClass:
				both("[^" + wordClass + "]")
			default:
				_, size := utf8.DecodeRuneInString(pat[i+1:])
				both(pat[i : i+1+size])
				i += 1 + size
				continue
			}
			i += 2
			continue
		case inClass && c == '[' && i+1 < len(pat) && pat[i+1] == ':': // [:alpha:]
			if end := strings.Index(pat[i:], ":]"); end >= 0 {
				both(pat[i : i+end+2])
				i += end + 2
				continue
			}
		case !inClass && c == '[':
			inClass = true
			both("[")
			i++
			if i < len(pat) && pat[i] == '^' {
				both("^")
				i++
			}
			if i < len(pat) && pat[i] == ']' { // a leading ] is literal
				both("]")
				i++
			}
			continue
		case inClass && c == ']':
			inClass = false
		case !inClass && c == '(':
			rest := pat[i+1:]
			named := strings.HasPrefix(rest, "?P<") || (strings.HasPrefix(rest, "?<") && !strings.HasPrefix(rest, "?<=") && !strings.HasPrefix(rest, "?<!"))
			if !strings.HasPrefix(rest, "?") || named {
				groups++
				user = append(user, groups)
			}
		}
		_, size := utf8.DecodeRuneInString(pat[i:])
		both(pat[i : i+size])
		i += size
	}
	return e.String(), p.String(), bounds, user
}

// literal returns the longest text (3+ runes) that every match of the tree
// contains, or "".
func literal(re *syntax.Regexp) string {
	switch re.Op {
	case syntax.OpLiteral:
		if s := string(re.Rune); utf8.RuneCountInString(s) >= 3 {
			return s
		}
	case syntax.OpCapture, syntax.OpPlus:
		return literal(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return literal(re.Sub[0])
		}
	case syntax.OpConcat:
		best := ""
		for _, sub := range re.Sub {
			if s := literal(sub); utf8.RuneCountInString(s) > utf8.RuneCountInString(best) {
				best = s
			}
		}
		return best
	}
	return ""
}

// Literal returns text that every match contains (3+ runes; letter case may
// differ when the pattern ignores case), or "". A search can use it to
// narrow the candidates with a substring index.
func (r *Regexp) Literal() string { return r.lit }

// ok reports whether the boundary groups of an engine match sit right.
func (r *Regexp) ok(s string, m []int) bool {
	for _, b := range r.bounds {
		at := m[2*b.group]
		if at < 0 {
			continue // the group's branch did not take part
		}
		if IsBoundary(s, at) == b.not {
			return false
		}
	}
	return true
}

// MatchString reports whether s contains a match.
func (r *Regexp) MatchString(s string) bool {
	if len(r.bounds) == 0 {
		return r.re.MatchString(s)
	}
	// Most texts have no candidate at all; then one cheap pass is enough.
	m := r.re.FindStringSubmatchIndex(s)
	if m == nil {
		return false
	}
	if r.ok(s, m) {
		return true
	}
	for _, m := range r.re.FindAllStringSubmatchIndex(s, -1)[1:] {
		if r.ok(s, m) {
			return true
		}
	}
	return false
}

// FindAllStringSubmatchIndex returns the matches in s, left to right and not
// overlapping, as index pairs: the whole match, then the caller's groups.
func (r *Regexp) FindAllStringSubmatchIndex(s string) [][]int {
	all := r.re.FindAllStringSubmatchIndex(s, -1)
	if len(r.bounds) == 0 {
		return all
	}
	out := all[:0]
	for _, m := range all {
		if !r.ok(s, m) {
			continue
		}
		u := make([]int, 0, 2*len(r.user))
		for _, g := range r.user {
			u = append(u, m[2*g], m[2*g+1])
		}
		out = append(out, u)
	}
	return out
}

// FindAllStringIndex returns the [start, end] of each match in s.
func (r *Regexp) FindAllStringIndex(s string) [][]int {
	all := r.FindAllStringSubmatchIndex(s)
	out := make([][]int, len(all))
	for i, m := range all {
		out[i] = m[:2:2]
	}
	return out
}

// Expand appends the replacement template for one match (from
// FindAllStringSubmatchIndex) to dst: $1 or ${name} is a group, $$ a dollar.
//
// Unlike Go's own templates, $1 ends at the last digit: "$1у" is group 1
// followed by "у", not a group named "1у".
func (r *Regexp) Expand(dst []byte, template, s string, m []int) []byte {
	return r.shadow.ExpandString(dst, braceDigits(template), s, m)
}

// braceDigits rewrites $1 to ${1}.
func braceDigits(t string) string {
	if !strings.Contains(t, "$") {
		return t
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '$' || i+1 == len(t) {
			b.WriteByte(t[i])
			continue
		}
		if t[i+1] == '$' {
			b.WriteString("$$")
			i++
			continue
		}
		j := i + 1
		for j < len(t) && t[j] >= '0' && t[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			continue
		}
		b.WriteString("${" + t[i+1:j] + "}")
		i = j - 1
	}
	return b.String()
}

// ReplaceAllString replaces every match in s with the expanded template.
func (r *Regexp) ReplaceAllString(s, template string) string {
	ms := r.FindAllStringSubmatchIndex(s)
	if len(ms) == 0 {
		return s
	}
	var out []byte
	last := 0
	for _, m := range ms {
		out = append(out, s[last:m[0]]...)
		out = r.Expand(out, template, s, m)
		last = m[1]
	}
	return string(append(out, s[last:]...))
}

// IsWordRune reports whether r is part of a word: a letter, digit, mark or underscore.
func IsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func isApostrophe(r rune) bool { return r == '\'' || r == '’' || r == 'ʼ' }

// JoinsWord reports whether the rune just before (or after) position i of s
// belongs to a word, counting an apostrophe between two word runes as part
// of it.
func JoinsWord(s string, i int, before bool) bool {
	var r rune
	var size int
	if before {
		r, size = utf8.DecodeLastRuneInString(s[:i])
	} else {
		r, size = utf8.DecodeRuneInString(s[i:])
	}
	if size == 0 {
		return false
	}
	if IsWordRune(r) {
		return true
	}
	if !isApostrophe(r) {
		return false
	}
	// An apostrophe counts when letters stand on both sides of it.
	var prev, next rune
	if before {
		prev, _ = utf8.DecodeLastRuneInString(s[:i-size])
		next, _ = utf8.DecodeRuneInString(s[i:])
	} else {
		prev, _ = utf8.DecodeLastRuneInString(s[:i])
		next, _ = utf8.DecodeRuneInString(s[i+size:])
	}
	return prev != utf8.RuneError && next != utf8.RuneError && IsWordRune(prev) && IsWordRune(next)
}

// IsBoundary reports whether position i of s is a word boundary: a word on
// exactly one side.
func IsBoundary(s string, i int) bool {
	return JoinsWord(s, i, true) != JoinsWord(s, i, false)
}

// cache holds compiled patterns: a search compiles its pattern once, however
// many rows it is run on.
var cache struct {
	sync.Mutex
	m map[string]cached
}

type cached struct {
	re  *Regexp
	err error
}

// Cached is Compile with a small process-wide cache.
func Cached(pat string) (*Regexp, error) {
	cache.Lock()
	defer cache.Unlock()
	if c, ok := cache.m[pat]; ok {
		return c.re, c.err
	}
	if len(cache.m) >= 256 || cache.m == nil {
		cache.m = map[string]cached{}
	}
	re, err := Compile(pat)
	cache.m[pat] = cached{re, err}
	return re, err
}
