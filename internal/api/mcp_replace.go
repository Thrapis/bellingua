package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/store"
)

// replace_in_targets: a server-side find/replace over translations, for
// terminology sweeps. Candidates come from the ordinary list filter; every
// change goes through Server.save like save_translation, so TM, QA masks and
// history stay in step.

const (
	maxReplacePairs    = 50
	defaultReplaceCap  = 500
	maxReplaceCap      = 2000
	maxReplaceSamples  = 10
	maxReplaceFailures = 50
	sampleRunes        = 300
)

type replacePair struct {
	Find    string `json:"find" jsonschema:"the text to find (plain string, not a pattern)"`
	Replace string `json:"replace" jsonschema:"what to put instead; may be empty"`
}

type replaceIn struct {
	filterIn
	Pairs      []replacePair `json:"pairs" jsonschema:"1-50 find/replace pairs, applied in order to each target"`
	IgnoreCase bool          `json:"ignoreCase,omitempty" jsonschema:"match regardless of letter case (default: case-sensitive); the replacement is inserted as given"`
	WholeWord  bool          `json:"wholeWord,omitempty" jsonschema:"only match whole words: the neighbours must not be letters, digits or an apostrophe inside a word"`
	DryRun     *bool         `json:"dryRun,omitempty" jsonschema:"default true: report what would change and write nothing; pass false to write"`
	Limit      int           `json:"limit,omitempty" jsonschema:"most matching units handled per call, 1-2000 (default 500)"`
	Cursor     string        `json:"cursor,omitempty" jsonschema:"the next value of the previous call"`
}

type mcpSweepSample struct {
	ID     int64  `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type mcpSweepFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

type mcpSweep struct {
	DryRun   bool              `json:"dryRun"`
	Matched  int               `json:"matched"` // units where some pair matches
	Changed  int               `json:"changed"` // saved (or, in a dry run, would be)
	Skipped  int               `json:"skipped"` // matched but approved
	Failed   int               `json:"failed"`
	Samples  []mcpSweepSample  `json:"samples"`
	Failures []mcpSweepFailure `json:"failures"`
	Next     string            `json:"next,omitempty"` // cursor when the limit stopped the sweep
}

// replacer applies one pair.
type replacer struct {
	re                   *regexp.Regexp
	repl                 string
	whole                bool
	checkStart, checkEnd bool // the find text starts/ends with a word character
}

func newReplacer(p replacePair, ignoreCase, whole bool) (*replacer, error) {
	if p.Find == "" {
		return nil, badRequest("find must not be empty")
	}
	expr := regexp.QuoteMeta(p.Find)
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, badRequest("bad find %q", p.Find)
	}
	first, _ := utf8.DecodeRuneInString(p.Find)
	last, _ := utf8.DecodeLastRuneInString(p.Find)
	return &replacer{re: re, repl: p.Replace, whole: whole, checkStart: isWordRune(first), checkEnd: isWordRune(last)}, nil
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func isApostrophe(r rune) bool { return r == '\'' || r == '’' || r == 'ʼ' }

// joinsWord reports whether the rune just before (or after) a match belongs
// to a word, counting an apostrophe between two letters as part of it.
func joinsWord(s string, i int, before bool) bool {
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
	if isWordRune(r) {
		return true
	}
	if !isApostrophe(r) {
		return false
	}
	var n rune
	if before {
		n, _ = utf8.DecodeLastRuneInString(s[:i-size])
	} else {
		n, _ = utf8.DecodeRuneInString(s[i+size:])
	}
	return n != utf8.RuneError && isWordRune(n)
}

func (r *replacer) apply(s string) string {
	locs := r.re.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if r.whole && ((r.checkStart && joinsWord(s, loc[0], true)) || (r.checkEnd && joinsWord(s, loc[1], false))) {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(r.repl)
		last = loc[1]
	}
	if last == 0 && b.Len() == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// cutAround shortens a before/after pair to about sampleRunes runes around
// the first difference.
func cutAround(before, after string) (string, string) {
	b, a := []rune(before), []rune(after)
	if len(b) <= sampleRunes && len(a) <= sampleRunes {
		return before, after
	}
	p := 0
	for p < len(b) && p < len(a) && b[p] == a[p] {
		p++
	}
	start := max(0, p-sampleRunes/3)
	clip := func(r []rune) string {
		end := min(len(r), start+sampleRunes)
		out := string(r[min(start, end):end])
		if start > 0 {
			out = "…" + out
		}
		if end < len(r) {
			out += "…"
		}
		return out
	}
	return clip(b), clip(a)
}

func (s *Server) replaceInTargets(ctx context.Context, in replaceIn) (mcpSweep, error) {
	out := mcpSweep{DryRun: in.DryRun == nil || *in.DryRun, Samples: []mcpSweepSample{}, Failures: []mcpSweepFailure{}}
	if n := len(in.Pairs); n == 0 || n > maxReplacePairs {
		return out, badRequest("pairs must hold 1-%d entries, not %d", maxReplacePairs, n)
	}
	reps := make([]*replacer, len(in.Pairs))
	for i, p := range in.Pairs {
		r, err := newReplacer(p, in.IgnoreCase, in.WholeWord)
		if err != nil {
			return out, err
		}
		reps[i] = r
	}
	if _, err := s.st.Project(ctx, in.Project); err != nil {
		return out, err
	}
	if len(in.States) == 0 {
		in.States = []string{"mt", "translated"}
	}
	// Candidates are found by searching the targets for the find text. With
	// several pairs one query can't cover them all, so the caller must give a
	// query or narrow the units by folder or file.
	if in.Query == "" {
		if len(in.Pairs) > 1 && in.Dir == "" && in.File == 0 {
			return out, badRequest("with several pairs give a query, dir or file to choose the units to scan")
		}
		if len(in.Pairs) == 1 {
			in.Query, in.In = in.Pairs[0].Find, "target"
		}
	}
	if in.In == "" {
		in.In = "target"
	}
	f, err := in.filter()
	if err != nil {
		return out, err
	}
	after, err := parseCursor(in.Cursor)
	if err != nil {
		return out, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultReplaceCap
	}
	limit = min(limit, maxReplaceCap)

	budget := limit
	for budget > 0 {
		// A page never holds more rows than the budget, so a page that spends
		// it ends exactly at the cursor List returns.
		rows, next, err := s.st.List(ctx, f, store.SortFile, after, min(budget, 200))
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if r.Target == nil {
				continue
			}
			src, cur := r.Source, model.Plain(r.Target)
			if r.Truncated { // the row carries previews; fetch whole texts
				u, err := s.st.Unit(ctx, r.ID, false)
				if err != nil {
					return out, err
				}
				src, cur = u.Source, model.Plain(u.Target)
			}
			repl := cur
			for _, rp := range reps {
				repl = rp.apply(repl)
			}
			if repl == cur {
				continue
			}
			out.Matched++
			budget--
			if r.State >= model.Approved {
				out.Skipped++
				continue
			}
			var saveErr error
			if _, err := model.SplitTarget(repl, src); err != nil {
				saveErr = err
			} else if !out.DryRun {
				_, saveErr = s.save(ctx, r.ID, store.Save{Target: &repl, State: r.State, Origin: mcpOrigin, IfBelow: model.Approved})
			}
			switch {
			case errors.Is(saveErr, store.ErrSkipped):
				out.Skipped++
			case saveErr != nil:
				out.Failed++
				if len(out.Failures) < maxReplaceFailures {
					out.Failures = append(out.Failures, mcpSweepFailure{ID: r.ID, Error: s.replaceError(saveErr)})
				}
			default:
				out.Changed++
				if len(out.Samples) < maxReplaceSamples {
					b, a := cutAround(cur, repl)
					out.Samples = append(out.Samples, mcpSweepSample{ID: r.ID, Before: b, After: a})
				}
			}
		}
		after = next
		if next == nil {
			break
		}
	}
	if budget == 0 && after != nil {
		out.Next = fmt.Sprintf("%d.%d", after.A, after.B)
	}
	return out, nil
}

// replaceError words a failed unit for the agent.
func (s *Server) replaceError(err error) string {
	var mm *model.CodeMismatch
	if errors.As(err, &mm) {
		return fmt.Sprintf("the replacement breaks the codes (missing %q, extra %q); the unit is left as it was", mm.Missing, mm.Extra)
	}
	return s.mcpError("replace_in_targets", err).Error()
}
