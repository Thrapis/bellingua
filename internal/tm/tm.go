// Package tm finds fuzzy translation-memory matches: human translations
// (translated/approved units) whose source is similar to a given source.
//
// Each project gets an in-memory inverted index from words to units, built
// lazily from the database and then kept current with Put/Remove as units
// are saved. Bulk changes (imports, bulk state changes) Invalidate it and
// the next search rebuilds it. Exact (100%) matches don't depend on this
// index: they come straight from the database (store.ExactMatches).
//
// Similarity is word-level: 1 - editDistance(words) / max(len). Words are
// lower-cased text tokens; markup (code pieces) is ignored.
package tm

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/Thrapis/bellingua/internal/model"
)

// Match is a fuzzy match.
type Match struct {
	UnitID int64
	Score  int // percent, 1..99 (100 is reserved for exact source equality)
}

const (
	minScore      = 60  // below this a match is noise
	maxWords      = 300 // longer sources only get exact matches
	maxCandidates = 60  // scored per query
)

// Words splits the text pieces into lower-cased word tokens.
func Words(ps []model.Piece) []string {
	var out []string
	for _, p := range ps {
		if p.Code {
			continue
		}
		f := strings.FieldsFunc(strings.ToLower(p.Text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		out = append(out, f...)
	}
	return out
}

// Index is one project's fuzzy index. Safe for concurrent use.
type Index struct {
	mu       sync.RWMutex
	words    map[int64][]string // unit -> its words
	postings map[string][]int64 // word -> units (may hold stale ids; checked against words)
}

func newIndex() *Index {
	return &Index{words: map[int64][]string{}, postings: map[string][]int64{}}
}

// Put adds or replaces a unit's source.
func (ix *Index) Put(id int64, source []model.Piece) {
	ws := Words(source)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	// Words already posted for this unit need no new posting. Postings of
	// words the unit no longer has go stale; Search checks against words.
	posted := map[string]bool{}
	for _, w := range ix.words[id] {
		posted[w] = true
	}
	ix.words[id] = ws
	for _, w := range ws {
		if !posted[w] {
			ix.postings[w] = append(ix.postings[w], id)
			posted[w] = true
		}
	}
}

// Remove drops a unit (e.g. its translation was cleared).
func (ix *Index) Remove(id int64) {
	ix.mu.Lock()
	delete(ix.words, id)
	ix.mu.Unlock()
}

// Len is the number of indexed units.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.words)
}

// Search returns up to limit fuzzy matches for source, best first.
func (ix *Index) Search(source []model.Piece, except int64, limit int) []Match {
	q := Words(source)
	if len(q) == 0 || len(q) > maxWords {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// Collect candidates sharing words with the query. Very common words
	// ("и", "в") would pull in half the corpus, so they are skipped unless
	// the query has nothing else.
	common := max(len(ix.words)/10, 2000)
	uniq := map[string]bool{}
	for _, w := range q {
		uniq[w] = true
	}
	hits := map[int64]int{}
	count := func(skipCommon bool) {
		for w := range uniq {
			ps := ix.postings[w]
			if skipCommon && len(ps) > common {
				continue
			}
			for _, id := range ps {
				if id != except {
					hits[id]++
				}
			}
		}
	}
	count(true)
	if len(hits) == 0 {
		count(false)
	}

	// A match needs at least minScore% of the words; shared-word counts give
	// an upper bound, so weak candidates are dropped before any distance.
	type cand struct {
		id   int64
		hits int
	}
	var cands []cand
	for id, h := range hits {
		ws, ok := ix.words[id]
		if !ok || len(ws) == 0 {
			continue // stale posting
		}
		longest := max(len(ws), len(q))
		if h*100 < minScore*longest-100 {
			continue
		}
		cands = append(cands, cand{id, h})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].hits != cands[j].hits {
			return cands[i].hits > cands[j].hits
		}
		return cands[i].id > cands[j].id
	})
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}

	var out []Match
	for _, c := range cands {
		ws := ix.words[c.id]
		d := editDistance(q, ws)
		score := 100 - (d*100+max(len(q), len(ws))-1)/max(len(q), len(ws)) // rounded down
		if score >= 100 {
			score = 99 // same words, different punctuation/markup/case
		}
		if score >= minScore {
			out = append(out, Match{c.id, score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].UnitID > out[j].UnitID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// editDistance is the word-level Levenshtein distance.
func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// Loader streams a project's human translations.
type Loader func(ctx context.Context, projectID int64, fn func(id int64, source []model.Piece) error) error

// Manager owns the per-project indexes.
type Manager struct {
	load Loader
	mu   sync.Mutex
	idx  map[int64]*entry
}

type entry struct {
	once sync.Once
	ix   *Index
	err  error
}

// NewManager creates a manager that builds indexes with load.
func NewManager(load Loader) *Manager {
	return &Manager{load: load, idx: map[int64]*entry{}}
}

// Get returns the project's index, building it on first use. Concurrent
// callers share one build.
func (m *Manager) Get(ctx context.Context, projectID int64) (*Index, error) {
	m.mu.Lock()
	e, ok := m.idx[projectID]
	if !ok {
		e = &entry{}
		m.idx[projectID] = e
	}
	m.mu.Unlock()
	e.once.Do(func() {
		ix := newIndex()
		e.err = m.load(ctx, projectID, func(id int64, src []model.Piece) error {
			ix.Put(id, src)
			return nil
		})
		e.ix = ix
	})
	if e.err != nil {
		m.Invalidate(projectID) // retry next time
		return nil, e.err
	}
	return e.ix, nil
}

// Update keeps a built index current after a unit was saved: human
// translations are indexed, anything else is removed. A project whose index
// is not built yet is left alone (the build will read the new state).
func (m *Manager) Update(projectID, unitID int64, source []model.Piece, human bool) {
	m.mu.Lock()
	e, ok := m.idx[projectID]
	m.mu.Unlock()
	if !ok || e.ix == nil {
		return
	}
	if human {
		e.ix.Put(unitID, source)
	} else {
		e.ix.Remove(unitID)
	}
}

// Invalidate drops a project's index; the next Get rebuilds it.
func (m *Manager) Invalidate(projectID int64) {
	m.mu.Lock()
	delete(m.idx, projectID)
	m.mu.Unlock()
}
