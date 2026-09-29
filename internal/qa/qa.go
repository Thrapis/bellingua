// Package qa runs quality checks on a translation. Every check has one bit in
// a mask; the mask is stored per unit and indexed for filtering, while the
// human-readable details are recomputed on demand (checks are pure and cheap).
package qa

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Thrapis/bellingua/internal/glossary"
	"github.com/Thrapis/bellingua/internal/model"
)

// Check is one bit of a unit's QA mask.
type Check uint32

const (
	Placeholders Check = 1 << iota // target codes differ from source codes
	Empty                          // target empty although state >= translated
	Whitespace                     // leading/trailing whitespace differs
	Punctuation                    // trailing punctuation differs
	Numbers                        // a number of the source is missing
	DoubleSpace                    // double space not present in source
	Brackets                       // unbalanced brackets or quotes
	Untranslated                   // target identical to a source with letters
	Forbidden                      // a letter forbidden in the target language
	Length                         // target much longer than source
	Terms                          // a glossary term is not translated as agreed
	// New checks go last: the bits are stored in units.qa.
)

// Errors are checks that make a unit unusable; the rest are warnings.
const Errors = Placeholders | Empty

// Info describes a check for the UI.
type Info struct {
	Check    Check  `json:"bit"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// All lists every check in bit order.
var All = []Info{
	{Placeholders, "placeholders", "Placeholders", "error"},
	{Empty, "empty", "Empty translation", "error"},
	{Whitespace, "whitespace", "Leading/trailing whitespace", "warning"},
	{Punctuation, "punctuation", "Trailing punctuation", "warning"},
	{Numbers, "numbers", "Numbers", "warning"},
	{DoubleSpace, "double-space", "Double space", "warning"},
	{Brackets, "brackets", "Brackets and quotes", "warning"},
	{Untranslated, "untranslated", "Same as source", "warning"},
	{Forbidden, "forbidden", "Forbidden letters", "warning"},
	{Length, "length", "Length", "warning"},
	{Terms, "glossary", "Glossary terms", "warning"},
}

// ByID maps a check id to its bit.
func ByID(id string) (Check, bool) {
	for _, i := range All {
		if i.ID == id {
			return i.Check, true
		}
	}
	return 0, false
}

// Config tunes the checks for a project.
type Config struct {
	Forbidden      string   `json:"forbidden"`      // letters that must not occur in target text (case-insensitive)
	MaxLengthRatio float64  `json:"maxLengthRatio"` // target/source length; 0 disables
	Disabled       []string `json:"disabled"`       // check ids to skip
}

// DefaultConfig returns sensible defaults for a target language.
func DefaultConfig(targetLang string) Config {
	c := Config{MaxLengthRatio: 2}
	if targetLang == "be" {
		c.Forbidden = "ищъ" // Russian letters that do not exist in Belarusian
	}
	return c
}

// Issue is one finding.
type Issue struct {
	Check    string `json:"check"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Unit is what a check looks at.
type Unit struct {
	Source []model.Piece
	Target []model.Piece // nil = no translation
	State  model.State
}

// Runner applies a Config; build once, use from many goroutines.
type Runner struct {
	disabled  Check
	forbidden map[rune]bool
	maxRatio  float64
	gloss     *glossary.Glossary
}

// WithGlossary returns a copy of r that also checks glossary terms.
func (r *Runner) WithGlossary(g *glossary.Glossary) *Runner {
	c := *r
	c.gloss = g
	return &c
}

// NewRunner prepares cfg.
func NewRunner(cfg Config) *Runner {
	r := &Runner{maxRatio: cfg.MaxLengthRatio, forbidden: map[rune]bool{}}
	for _, id := range cfg.Disabled {
		if c, ok := ByID(id); ok {
			r.disabled |= c
		}
	}
	for _, ch := range cfg.Forbidden {
		r.forbidden[unicode.ToLower(ch)] = true
	}
	return r
}

// Mask returns only the bit mask (the hot path during import).
func (r *Runner) Mask(u Unit) Check {
	m, _ := r.run(u, false)
	return m
}

// Run returns the mask and the details.
func (r *Runner) Run(u Unit) (Check, []Issue) { return r.run(u, true) }

func (r *Runner) run(u Unit, detail bool) (Check, []Issue) {
	var mask Check
	var issues []Issue
	add := func(c Check, format string, args ...any) {
		if r.disabled&c != 0 || mask&c != 0 {
			return
		}
		mask |= c
		if detail {
			info := infoOf(c)
			issues = append(issues, Issue{info.ID, info.Severity, fmt.Sprintf(format, args...)})
		}
	}

	if u.Target == nil {
		return 0, nil
	}
	src, tgt := model.Plain(u.Source), model.Plain(u.Target)
	if tgt == "" {
		if u.State >= model.Translated && src != "" {
			add(Empty, "translation is empty")
		}
		return mask, issues
	}

	if _, err := model.SplitTarget(tgt, u.Source); err != nil {
		add(Placeholders, "%s", err.Error())
	}

	srcText, tgtText := textOnly(u.Source), textOnly(u.Target)

	if lead(src) != lead(tgt) || trail(src) != trail(tgt) {
		add(Whitespace, "leading/trailing whitespace differs from source")
	}
	if a, b := endPunct(srcText), endPunct(tgtText); a != b {
		add(Punctuation, "source ends with %q, translation with %q", a, b)
	}
	if missing := missingNumbers(srcText, tgtText); len(missing) > 0 {
		add(Numbers, "numbers missing in translation: %s", strings.Join(missing, ", "))
	}
	if strings.Contains(tgtText, "  ") && !strings.Contains(srcText, "  ") {
		add(DoubleSpace, "double space")
	}
	if msg := unbalanced(tgtText); msg != "" && unbalanced(srcText) == "" {
		add(Brackets, "%s", msg)
	}
	if src == tgt && hasLetter(srcText) {
		add(Untranslated, "translation is identical to source")
	}
	if len(r.forbidden) > 0 {
		var found []string
		seen := map[rune]bool{}
		for _, ch := range tgtText {
			l := unicode.ToLower(ch)
			if r.forbidden[l] && !seen[l] {
				seen[l] = true
				found = append(found, string(ch))
			}
		}
		if len(found) > 0 {
			add(Forbidden, "forbidden letters: %s", strings.Join(found, " "))
		}
	}
	if r.gloss.Len() > 0 && r.disabled&Terms == 0 {
		if problems := r.gloss.Check(u.Source, u.Target); len(problems) > 0 {
			var parts []string
			for _, p := range problems {
				if p.Term.DNT {
					parts = append(parts, fmt.Sprintf("«%s» should stay untranslated", p.Term.Source))
				} else {
					parts = append(parts, fmt.Sprintf("«%s» → «%s»", p.Term.Source, strings.Join(p.Term.Targets(), "» / «")))
				}
			}
			add(Terms, "glossary: %s", strings.Join(parts, "; "))
		}
	}
	if r.maxRatio > 0 {
		sl, tl := utf8.RuneCountInString(srcText), utf8.RuneCountInString(tgtText)
		if sl >= 10 && float64(tl) > float64(sl)*r.maxRatio {
			add(Length, "translation is %.1f× longer than source", float64(tl)/float64(sl))
		}
	}
	return mask, issues
}

func infoOf(c Check) Info {
	for _, i := range All {
		if i.Check == c {
			return i
		}
	}
	return Info{}
}

// Describe expands a mask into issue stubs (without messages).
func Describe(mask Check) []Info {
	var out []Info
	for _, i := range All {
		if mask&i.Check != 0 {
			out = append(out, i)
		}
	}
	return out
}

// textOnly joins the text pieces. Codes at either end are dropped; an inner
// code becomes U+2060 (word joiner), which keeps the text on both sides apart
// without looking like whitespace or punctuation to the checks.
func textOnly(ps []model.Piece) string {
	if len(ps) == 1 && !ps[0].Code {
		return ps[0].Text
	}
	var b strings.Builder
	for _, p := range ps {
		if p.Code {
			b.WriteByte(0)
		} else {
			b.WriteString(p.Text)
		}
	}
	// Codes at the very ends don't count as text; inner ones become spaces.
	s := strings.Trim(b.String(), "\x00")
	return strings.ReplaceAll(s, "\x00", "⁠")
}

func lead(s string) string  { return s[:len(s)-len(strings.TrimLeftFunc(s, unicode.IsSpace))] }
func trail(s string) string { return s[len(strings.TrimRightFunc(s, unicode.IsSpace)):] }

const puncts = ".!?…:;,"

func endPunct(s string) string {
	s = strings.TrimRightFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '⁠' || strings.ContainsRune(`"'»”)]`, r) })
	i := len(s)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if !strings.ContainsRune(puncts, r) {
			break
		}
		i -= size
	}
	p := s[i:]
	// "..." and "…" are the same ending.
	return strings.ReplaceAll(p, "...", "…")
}

var numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)*`)

func missingNumbers(src, tgt string) []string {
	want := numberRe.FindAllString(src, -1)
	if len(want) == 0 {
		return nil
	}
	have := map[string]int{}
	for _, n := range numberRe.FindAllString(tgt, -1) {
		have[normNum(n)]++
	}
	var missing []string
	for _, n := range want {
		k := normNum(n)
		if have[k] > 0 {
			have[k]--
		} else {
			missing = append(missing, n)
		}
	}
	return missing
}

func normNum(n string) string { return strings.ReplaceAll(n, ",", ".") }

var pairs = []struct{ open, close rune }{{'(', ')'}, {'[', ']'}, {'«', '»'}, {'„', '“'}}

func unbalanced(s string) string {
	for _, p := range pairs {
		if strings.Count(s, string(p.open)) != strings.Count(s, string(p.close)) {
			return fmt.Sprintf("unbalanced %c %c", p.open, p.close)
		}
	}
	if strings.Count(s, `"`)%2 != 0 {
		return `unbalanced "`
	}
	return ""
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
