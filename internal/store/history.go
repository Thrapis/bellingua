package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Thrapis/bellingua/internal/model"
)

// NewBatch returns an id that ties the history rows of one operation together.
func NewBatch(kind string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", kind, time.Now().Unix(), hex.EncodeToString(b[:]))
}

// The project history feed reads history rows that replaced something
// (old_state IS NOT NULL): the synthetic 'import' baseline is not a change.
// Spelled exactly like this so the partial index history_proj applies.
//
// Rows saved one by one have no batch; they are grouped by origin and hour.
const batchKey = "COALESCE(batch, origin || '@' || (at / 3600))"

// HistoryTransition counts the units that went from one state to another.
type HistoryTransition struct {
	Old   model.State `json:"old"`
	New   model.State `json:"new"`
	Count int64       `json:"count"`
}

// HistoryBatch is the changes of one origin in one operation (or hour).
type HistoryBatch struct {
	Key         string              `json:"key"` // pass as the list's batch filter
	Origin      string              `json:"origin"`
	Count       int64               `json:"count"`
	First       int64               `json:"first"`
	Last        int64               `json:"last"`
	Transitions []HistoryTransition `json:"transitions"`
}

// HistoryDay is one local calendar day of changes.
type HistoryDay struct {
	Start   int64          `json:"start"` // unix time of the day's local midnight
	Count   int64          `json:"count"`
	Batches []HistoryBatch `json:"batches"`
}

// HistoryDays returns up to days days of changes older than before (unix
// time; 0 = all), newest first. tz is the viewer's offset east of UTC in
// seconds. next is the cutoff for the following page, 0 when there is none.
func (s *Store) HistoryDays(ctx context.Context, projectID, tz, before int64, days int) (out []HistoryDay, next int64, err error) {
	if before <= 0 {
		before = 1 << 60
	}
	for len(out) < days {
		var last sql.NullInt64
		err = s.R.QueryRowContext(ctx, `SELECT max(at) FROM history WHERE project_id = ? AND old_state IS NOT NULL AND at < ?`,
			projectID, before).Scan(&last)
		if err != nil {
			return nil, 0, err
		}
		if !last.Valid {
			return out, 0, nil
		}
		start := (last.Int64+tz)/86400*86400 - tz
		d, err := s.historyDay(ctx, projectID, start, before)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
		before = start
	}
	var more int
	err = s.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM history WHERE project_id = ? AND old_state IS NOT NULL AND at < ?)`,
		projectID, before).Scan(&more)
	if err != nil {
		return nil, 0, err
	}
	if more == 1 {
		next = before
	}
	return out, next, nil
}

// historyDay aggregates the rows with start <= at < end (end caps the day when
// it is the first page's partial day).
func (s *Store) historyDay(ctx context.Context, projectID, start, end int64) (HistoryDay, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+batchKey+`, origin, old_state, state, count(*), min(at), max(at)
		FROM history WHERE project_id = ? AND old_state IS NOT NULL AND at >= ? AND at < ?
		GROUP BY 1, origin, old_state, state`, projectID, start, end)
	if err != nil {
		return HistoryDay{}, err
	}
	defer rows.Close()
	d := HistoryDay{Start: start}
	idx := map[string]*HistoryBatch{}
	var order []*HistoryBatch
	for rows.Next() {
		var key, origin string
		var t HistoryTransition
		var first, last int64
		if err := rows.Scan(&key, &origin, &t.Old, &t.New, &t.Count, &first, &last); err != nil {
			return d, err
		}
		b := idx[key+"\x00"+origin]
		if b == nil {
			b = &HistoryBatch{Key: key, Origin: origin, First: first, Last: last}
			idx[key+"\x00"+origin] = b
			order = append(order, b)
		}
		b.Count += t.Count
		b.First, b.Last = min(b.First, first), max(b.Last, last)
		b.Transitions = append(b.Transitions, t)
		d.Count += t.Count
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	sort.Slice(order, func(i, j int) bool { return order[i].Last > order[j].Last })
	for _, b := range order {
		sort.Slice(b.Transitions, func(i, j int) bool { return b.Transitions[i].Count > b.Transitions[j].Count })
		d.Batches = append(d.Batches, *b)
	}
	return d, nil
}

// HistoryFilter narrows the history feed.
type HistoryFilter struct {
	ProjectID int64
	From, To  int64    // from <= at < to; zero = open
	Origins   []string // empty = all
	Batch     string   // a HistoryBatch.Key
	State     *model.State
	FileID    int64
	Dir       string
}

// HistoryRow is one change in the feed.
type HistoryRow struct {
	ID       int64       `json:"id"`
	UnitID   int64       `json:"unit"`
	FileID   int64       `json:"file"`
	Path     string      `json:"path"`
	Key      string      `json:"key"`
	Origin   string      `json:"origin"`
	At       int64       `json:"at"`
	Batch    string      `json:"batch,omitempty"`
	OldState model.State `json:"oldState"`
	State    model.State `json:"state"`
	Old      *string     `json:"old"` // previews, cut to PreviewRunes
	New      *string     `json:"new"`
}

// HistoryList pages the feed newest first with a keyset cursor (at, id).
func (s *Store) HistoryList(ctx context.Context, f HistoryFilter, after *Cursor, limit int) ([]HistoryRow, *Cursor, error) {
	var b strings.Builder
	b.WriteString("h.project_id = ? AND h.old_state IS NOT NULL")
	args := []any{f.ProjectID}
	if f.From > 0 {
		b.WriteString(" AND h.at >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		b.WriteString(" AND h.at < ?")
		args = append(args, f.To)
	}
	if len(f.Origins) > 0 {
		b.WriteString(" AND h.origin IN (?" + strings.Repeat(",?", len(f.Origins)-1) + ")")
		for _, o := range f.Origins {
			args = append(args, o)
		}
	}
	if f.Batch != "" {
		b.WriteString(" AND COALESCE(h.batch, h.origin || '@' || (h.at / 3600)) = ?")
		args = append(args, f.Batch)
	}
	if f.State != nil {
		b.WriteString(" AND h.state = ?")
		args = append(args, *f.State)
	}
	if f.FileID > 0 {
		b.WriteString(" AND u.file_id = ?")
		args = append(args, f.FileID)
	} else if f.Dir != "" {
		b.WriteString(" AND (f.dir = ? OR f.dir LIKE ? ESCAPE '\\')")
		args = append(args, f.Dir, escapeLike(f.Dir)+"/%")
	}
	if after != nil {
		b.WriteString(" AND (h.at < ? OR (h.at = ? AND h.id < ?))")
		args = append(args, after.A, after.A, after.B)
	}
	args = append(args, limit+1)
	rows, err := s.R.QueryContext(ctx, `SELECT h.id, h.unit_id, u.file_id, f.path, u.key, h.origin, h.at, COALESCE(h.batch, ''),
			h.old_state, h.state, substr(h.old_target, 1, `+fmt.Sprint(PreviewRunes)+`), substr(h.target, 1, `+fmt.Sprint(PreviewRunes)+`)
		FROM history h JOIN units u ON u.id = h.unit_id JOIN files f ON f.id = u.file_id
		WHERE `+b.String()+` ORDER BY h.at DESC, h.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var r HistoryRow
		var o, n sql.NullString
		if err := rows.Scan(&r.ID, &r.UnitID, &r.FileID, &r.Path, &r.Key, &r.Origin, &r.At, &r.Batch,
			&r.OldState, &r.State, &o, &n); err != nil {
			return nil, nil, err
		}
		if o.Valid {
			r.Old = &o.String
		}
		if n.Valid {
			r.New = &n.String
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *Cursor
	if len(out) > limit {
		out = out[:limit]
		next = &Cursor{A: out[limit-1].At, B: out[limit-1].ID}
	}
	return out, next, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
