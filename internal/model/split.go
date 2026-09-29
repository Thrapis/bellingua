package model

import (
	"sort"
	"strings"
)

// CodeMismatch describes why a target's codes do not match its source.
type CodeMismatch struct {
	Missing []string // source codes absent from the target
	Extra   []string // code texts that occur in the target more often than in the source
}

func (m *CodeMismatch) Error() string {
	var parts []string
	if len(m.Missing) > 0 {
		parts = append(parts, "missing "+strings.Join(quoteAll(m.Missing), ", "))
	}
	if len(m.Extra) > 0 {
		parts = append(parts, "extra "+strings.Join(quoteAll(m.Extra), ", "))
	}
	return "placeholders: " + strings.Join(parts, "; ")
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "«" + visible(s) + "»"
	}
	return out
}

func visible(s string) string {
	return strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(s)
}

// SplitTarget turns a plain target string back into pieces by locating the
// source's codes in it. Codes may be moved but must each appear exactly as
// many times as in the source. At every position the longest still-unused
// source code wins, so a code that is a prefix of another cannot steal its
// match. The pieces are always returned; err is a *CodeMismatch when the
// multisets differ.
func SplitTarget(target string, source []Piece) ([]Piece, error) {
	remaining := map[string]int{}
	var codes []string
	for _, p := range source {
		if p.Code && p.Text != "" {
			if remaining[p.Text] == 0 {
				codes = append(codes, p.Text)
			}
			remaining[p.Text]++
		}
	}
	if len(codes) == 0 {
		if target == "" {
			return []Piece{}, nil
		}
		return []Piece{{Text: target}}, nil
	}
	sort.Slice(codes, func(i, j int) bool { return len(codes[i]) > len(codes[j]) })

	var ps []Piece
	var text strings.Builder
	extra := map[string]int{}
	for i := 0; i < len(target); {
		matched := ""
		for _, c := range codes {
			if strings.HasPrefix(target[i:], c) {
				matched = c
				break
			}
		}
		if matched == "" {
			text.WriteByte(target[i])
			i++
			continue
		}
		if remaining[matched] == 0 {
			extra[matched]++
			text.WriteString(matched)
			i += len(matched)
			continue
		}
		remaining[matched]--
		if text.Len() > 0 {
			ps = append(ps, Piece{Text: text.String()})
			text.Reset()
		}
		ps = append(ps, Piece{Text: matched, Code: true})
		i += len(matched)
	}
	if text.Len() > 0 {
		ps = append(ps, Piece{Text: text.String()})
	}
	if ps == nil {
		ps = []Piece{}
	}

	var m CodeMismatch
	for _, c := range codes {
		for n := remaining[c]; n > 0; n-- {
			m.Missing = append(m.Missing, c)
		}
		for n := extra[c]; n > 0; n-- {
			m.Extra = append(m.Extra, c)
		}
	}
	if len(m.Missing) > 0 || len(m.Extra) > 0 {
		return ps, &m
	}
	return ps, nil
}
