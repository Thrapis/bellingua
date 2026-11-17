package api

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// list_words: the vocabulary of the translations (or sources), one entry per
// distinct word with its count, for hunting typos as a ranked list instead of
// reading pages of units.

const (
	defaultWordLimit = 200
	maxWordLimit     = 2000
)

type wordsIn struct {
	filterIn
	Field         string `json:"field,omitempty" jsonschema:"whose words: target (default) or source"`
	MinCount      int    `json:"minCount,omitempty" jsonschema:"only words occurring at least this often (default 1)"`
	MaxCount      int    `json:"maxCount,omitempty" jsonschema:"only words occurring at most this often; maxCount 1 lists the words that occur once, where typos hide"`
	CaseSensitive bool   `json:"caseSensitive,omitempty" jsonschema:"count Ганна and ганна apart (default: folded to lower case)"`
	Sort          string `json:"sort,omitempty" jsonschema:"count (default: most frequent first, ties alphabetical) or alpha"`
	Limit         int    `json:"limit,omitempty" jsonschema:"words per call, 1-2000 (default 200)"`
	Offset        int    `json:"offset,omitempty" jsonschema:"words to skip, to read the next page (the list is recomputed on every call)"`
}

type mcpWord struct {
	Word  string `json:"word"`
	Count int    `json:"count"`
}

type mcpWords struct {
	Words []mcpWord `json:"words"`
	Total int       `json:"total"` // distinct words after minCount/maxCount, before paging
	Units int       `json:"units"` // units scanned
}

func (s *Server) listWords(ctx context.Context, in wordsIn) (mcpWords, error) {
	out := mcpWords{Words: []mcpWord{}}
	var target bool
	switch in.Field {
	case "", "target":
		target = true
	case "source":
	default:
		return out, badRequest("field must be target or source, not %q", in.Field)
	}
	switch in.Sort {
	case "", "count", "alpha":
	default:
		return out, badRequest("sort must be count or alpha, not %q", in.Sort)
	}
	if in.Limit < 0 || in.Limit > maxWordLimit {
		return out, badRequest("limit must be 1-%d", maxWordLimit)
	}
	if in.Offset < 0 || in.MinCount < 0 || in.MaxCount < 0 {
		return out, badRequest("offset, minCount and maxCount must not be negative")
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultWordLimit
	}
	if _, err := s.st.Project(ctx, in.Project); err != nil {
		return out, err
	}
	f, err := in.filter()
	if err != nil {
		return out, err
	}
	counts := map[string]int{}
	n, err := s.st.ScanTexts(ctx, f, target, func(text string) {
		eachWord(text, func(w string) {
			if !in.CaseSensitive {
				w = strings.ToLower(w)
			}
			counts[w]++
		})
	})
	if err != nil {
		return out, err
	}
	out.Units = n
	all := make([]mcpWord, 0, len(counts))
	for w, c := range counts {
		if c >= max(in.MinCount, 1) && (in.MaxCount == 0 || c <= in.MaxCount) {
			all = append(all, mcpWord{w, c})
		}
	}
	if in.Sort == "alpha" {
		sort.Slice(all, func(i, j int) bool { return all[i].Word < all[j].Word })
	} else {
		sort.Slice(all, func(i, j int) bool {
			if all[i].Count != all[j].Count {
				return all[i].Count > all[j].Count
			}
			return all[i].Word < all[j].Word
		})
	}
	out.Total = len(all)
	if in.Offset < len(all) {
		out.Words = all[in.Offset:min(in.Offset+limit, len(all))]
	}
	return out, nil
}

// eachWord calls fn for every word of s: a run of letters, digits and marks,
// where a single apostrophe or hyphen between two such characters belongs to
// the word (аб'ект, аб’ект, то-бок). Everything else separates words.
func eachWord(s string, fn func(string)) {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }
	start := -1
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case isWord(r):
			if start < 0 {
				start = i
			}
		case start >= 0 && (r == '\'' || r == '’' || r == 'ʼ' || r == '-'):
			if next, _ := utf8.DecodeRuneInString(s[i+size:]); i+size < len(s) && isWord(next) {
				break // inside the word
			}
			fn(s[start:i])
			start = -1
		default:
			if start >= 0 {
				fn(s[start:i])
				start = -1
			}
		}
		i += size
	}
	if start >= 0 {
		fn(s[start:])
	}
}

// parseSince reads modifiedSince: a duration back from now ("90m", "48h"),
// unix seconds, a date ("2026-11-17", local midnight), a local date and time
// ("2026-11-17 09:30") or an RFC 3339 time.
func parseSince(v string, now time.Time) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			return 0, badRequest("modifiedSince %q must be a positive duration", v)
		}
		return now.Add(-d).Unix(), nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && len(v) >= 9 {
		return n, nil // a unix time, not a stray number
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, badRequest("modifiedSince %q: use a duration like 90m or 48h, a date like 2026-11-17, an RFC 3339 time or unix seconds", v)
}
