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
	"github.com/Thrapis/bellingua/internal/rx"
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
	Find    string `json:"find" jsonschema:"the text to find: a plain string, or with regex=true a pattern (see regex)"`
	Replace string `json:"replace" jsonschema:"what to put instead; may be empty. With regex=true it is a template: $1 or ${name} is a group of the match, $$ a dollar sign"`
}

type replaceIn struct {
	filterIn
	Pairs        []replacePair `json:"pairs" jsonschema:"1-50 find/replace pairs, applied in order to each target"`
	IgnoreCase   bool          `json:"ignoreCase,omitempty" jsonschema:"match regardless of letter case (default: case-sensitive); the replacement is inserted as given"`
	PreserveCase bool          `json:"preserveCase,omitempty" jsonschema:"give each replacement the letter case of the text it replaces: ALL CAPITALS stay capitals, a Capitalised word stays capitalised, lower stays lower, mixed case is inserted as written (use with ignoreCase)"`
	Full         bool          `json:"full,omitempty" jsonschema:"also return every matched unit as matches [{id, before, after, approved}] (texts cut to about 300 characters around the change), not just ten samples; at most limit units"`
	WholeWord    bool          `json:"wholeWord,omitempty" jsonschema:"only match whole words: the neighbours must not be letters, digits or an apostrophe inside a word"`
	DryRun       *bool         `json:"dryRun,omitempty" jsonschema:"default true: report what would change and write nothing; pass false to write"`
	Limit        int           `json:"limit,omitempty" jsonschema:"most matching units handled per call, 1-2000 (default 500)"`
	Cursor       string        `json:"cursor,omitempty" jsonschema:"the next value of the previous call"`
}

type mcpSweepSample struct {
	ID       int64  `json:"id"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Approved bool   `json:"approved,omitempty"` // the unit is approved, so it is never changed
}

// mcpSweepPair says what one pair found, so a result of 0 can be told apart.
type mcpSweepPair struct {
	Find        string `json:"find"`
	Units       int    `json:"units"`       // units where this pair matched
	Occurrences int    `json:"occurrences"` // matches in those units
	// AlreadyHave is set when the pair matched nothing: how many units (in
	// the same scope) already contain the replacement text. Many means the
	// sweep was probably done before; none means look for a typo in find.
	AlreadyHave int `json:"alreadyHave,omitempty"`
}

type mcpSweepFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

type mcpSweep struct {
	DryRun  bool `json:"dryRun"`
	Matched int  `json:"matched"` // units where some pair matches
	Changed int  `json:"changed"` // saved (or, in a dry run, would be)
	Skipped int  `json:"skipped"` // matched but approved
	Failed  int  `json:"failed"`
	// ApprovedMatches counts the matched units that are approved (and so left
	// alone); they are in matched and skipped too.
	ApprovedMatches int               `json:"approvedMatches"`
	Pairs           []mcpSweepPair    `json:"pairs"`
	Samples         []mcpSweepSample  `json:"samples"`
	Matches         []mcpSweepSample  `json:"matches,omitempty"` // every matched unit, with full=true
	Failures        []mcpSweepFailure `json:"failures"`
	SweepID         string            `json:"sweepId,omitempty"` // a real run's handle for revert_sweep
	Next            string            `json:"next,omitempty"`    // cursor when the limit stopped the sweep
}

// replacer applies one pair.
type replacer struct {
	find     string
	re       *rx.Regexp
	repl     string
	template bool // repl is a template with $1 groups (regex mode)
	whole    bool
	keepCase bool
	// units and occurrences count what the pair found over a whole sweep.
	units, occurrences int
}

func newReplacer(p replacePair, in replaceIn) (*replacer, error) {
	if p.Find == "" {
		return nil, badRequest("find must not be empty")
	}
	expr := p.Find
	if !in.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := rx.Compile(expr)
	if err != nil {
		return nil, badRequest("bad pattern %q: %v", p.Find, err)
	}
	return &replacer{find: p.Find, re: re, repl: p.Replace, template: in.Regex, whole: in.WholeWord, keepCase: in.PreserveCase}, nil
}

// matchCase gives repl the letter case of match: all capitals (two or more
// letters) -> all capitals; a capital first letter followed by lower case, a
// single capital, or a capital first letter with mixed case after it -> first
// letter capital; all lower case -> all lower case. Anything else (a lower
// case start with capitals inside, no letters) leaves repl as written.
func matchCase(match, repl string) string {
	letters, upper, lower := 0, 0, 0
	firstUpper := false
	for _, r := range match {
		switch {
		case unicode.IsUpper(r):
			if letters == 0 {
				firstUpper = true
			}
			letters++
			upper++
		case unicode.IsLower(r):
			letters++
			lower++
		}
	}
	switch {
	case letters == 0:
		return repl
	case upper == letters && letters >= 2:
		return strings.ToUpper(repl)
	case firstUpper:
		return capitalize(repl)
	case lower == letters:
		return strings.ToLower(repl)
	}
	return repl
}

func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// apply replaces the matches in s and counts them in r.
func (r *replacer) apply(s string) string {
	var out []byte
	last, n := 0, 0
	for _, m := range r.re.FindAllStringSubmatchIndex(s) {
		if m[0] == m[1] {
			continue // a pattern like x* matches nothing between any two letters
		}
		if r.whole {
			// An end of the match that is a word character must not continue into a word.
			first, _ := utf8.DecodeRuneInString(s[m[0]:])
			end, _ := utf8.DecodeLastRuneInString(s[:m[1]])
			if (rx.IsWordRune(first) && rx.JoinsWord(s, m[0], true)) || (rx.IsWordRune(end) && rx.JoinsWord(s, m[1], false)) {
				continue
			}
		}
		out = append(out, s[last:m[0]]...)
		repl := r.repl
		if r.template {
			repl = string(r.re.Expand(nil, r.repl, s, m))
		}
		if r.keepCase {
			repl = matchCase(s[m[0]:m[1]], repl)
		}
		out = append(out, repl...)
		last = m[1]
		n++
	}
	if n == 0 {
		return s
	}
	r.units++
	r.occurrences += n
	return string(append(out, s[last:]...))
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
	out := mcpSweep{DryRun: in.DryRun == nil || *in.DryRun, Pairs: []mcpSweepPair{}, Samples: []mcpSweepSample{}, Failures: []mcpSweepFailure{}}
	if n := len(in.Pairs); n == 0 || n > maxReplacePairs {
		return out, badRequest("pairs must hold 1-%d entries, not %d", maxReplacePairs, n)
	}
	reps := make([]*replacer, len(in.Pairs))
	for i, p := range in.Pairs {
		r, err := newReplacer(p, in)
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
			if in.Regex && in.IgnoreCase {
				in.Query = "(?i)" + in.Query
			}
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

	batch := ""
	if !out.DryRun {
		batch = store.NewBatch("sweep")
	}
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
			if in.Full {
				b, a := cutAround(cur, repl)
				out.Matches = append(out.Matches, mcpSweepSample{ID: r.ID, Before: b, After: a, Approved: r.State >= model.Approved})
			}
			if r.State >= model.Approved {
				out.ApprovedMatches++
				out.Skipped++
				continue
			}
			var saveErr error
			if _, err := model.SplitTarget(repl, src); err != nil {
				saveErr = err
			} else if !out.DryRun {
				_, saveErr = s.save(ctx, r.ID, store.Save{Target: &repl, State: r.State, Origin: mcpOrigin, IfBelow: model.Approved, Batch: batch})
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
	for _, rp := range reps {
		pr := mcpSweepPair{Find: rp.find, Units: rp.units, Occurrences: rp.occurrences}
		if rp.occurrences == 0 && !in.Regex && rp.repl != "" {
			// Tell "already done" from "wrong find text": how many units in
			// the same scope hold the replacement.
			hf := f
			hf.Query, hf.In, hf.Regex = rp.repl, "target", false
			if n, err := s.st.Count(ctx, hf); err == nil {
				pr.AlreadyHave = int(n)
			}
		}
		out.Pairs = append(out.Pairs, pr)
	}
	if !out.DryRun && out.Changed > 0 {
		out.SweepID = batch
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
