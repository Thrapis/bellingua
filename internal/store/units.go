package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Thrapis/bellingua/internal/model"
	"github.com/Thrapis/bellingua/internal/qa"
)

// Filter selects units of one project. Zero values mean "no constraint".
type Filter struct {
	ProjectID int64
	FileID    int64
	Dir       string // folder prefix ("a/b" matches a/b and a/b/...)
	Query     string
	In        string // source | target | both (default) | key | context
	States    []model.State
	QA        qa.Check // units having any of these bits; qa.Check(^0) = any issue
	Errors    bool     // units with a QA error
	Changed   bool     // source changed by a re-import
	SameAs    int64    // units whose source is identical to this unit's (itself included)
}

// Sort is a list order. Every order is served by an index and paged by
// keyset, so deep pages cost the same as the first.
type Sort string

const (
	SortFile    Sort = ""        // by file, then position in the file
	SortUpdated Sort = "updated" // most recently changed first
	SortShort   Sort = "short"   // shortest source first
	SortLong    Sort = "long"    // longest source first
)

// ParseSort validates a sort name ("" and "file" mean SortFile).
func ParseSort(s string) (Sort, bool) {
	switch Sort(s) {
	case SortFile, "file":
		return SortFile, true
	case SortUpdated, SortShort, SortLong:
		return Sort(s), true
	}
	return "", false
}

// order returns the two keyset columns, the ORDER BY clause and whether the
// order is descending.
func (s Sort) order() (a, b, orderBy string, desc bool) {
	switch s {
	case SortUpdated:
		return "u.updated_at", "u.id", "u.updated_at DESC, u.id DESC", true
	case SortShort:
		return "length(u.source)", "u.id", "length(u.source), u.id", false
	case SortLong:
		return "length(u.source)", "u.id", "length(u.source) DESC, u.id DESC", true
	}
	return "u.file_id", "u.ord", "u.project_id, u.file_id, u.ord", false
}

// Cursor is a keyset position: (file_id, ord) in file order, (sort value,
// id) otherwise. Clients treat it as opaque.
type Cursor struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

func (s Sort) cursorOf(r Row) Cursor {
	switch s {
	case SortUpdated:
		return Cursor{r.UpdatedAt, r.ID}
	case SortShort, SortLong:
		return Cursor{int64(r.SourceLen), r.ID}
	}
	return Cursor{r.FileID, r.Ord}
}

// where builds the WHERE clause (without "WHERE") and its args.
func (f Filter) where() (string, []any) {
	var b strings.Builder
	args := []any{f.ProjectID}
	b.WriteString("u.project_id = ? AND u.obsolete = 0")
	if f.FileID != 0 {
		b.WriteString(" AND u.file_id = ?")
		args = append(args, f.FileID)
	} else if f.Dir != "" {
		b.WriteString(` AND u.file_id IN (SELECT id FROM files WHERE project_id = ? AND (dir = ? OR substr(dir, 1, ?) = ?))`)
		args = append(args, f.ProjectID, f.Dir, len(f.Dir)+1, f.Dir+"/")
	}
	switch states := stateSet(f.States); {
	case states == 0 || states == 0b1111:
	case states == 0b0011:
		b.WriteString(" AND u.state < 2") // matches the units_todo partial index
	default:
		var in []string
		for s := model.Untranslated; s <= model.Approved; s++ {
			if states&(1<<s) != 0 {
				in = append(in, fmt.Sprint(int(s)))
			}
		}
		b.WriteString(" AND u.state IN (" + strings.Join(in, ",") + ")")
	}
	if f.QA != 0 || f.Errors {
		b.WriteString(" AND u.qa <> 0") // matches the units_qa partial index
		mask := f.QA
		if f.Errors {
			mask = qa.Errors
		}
		if mask != qa.Check(^uint32(0)) {
			b.WriteString(" AND (u.qa & ?) <> 0")
			args = append(args, int64(mask))
		}
	}
	if f.SameAs != 0 {
		// source_hash makes this an index lookup; source guards against collisions.
		b.WriteString(` AND u.source_hash = (SELECT source_hash FROM units WHERE id = ?)
			AND u.source = (SELECT source FROM units WHERE id = ?)`)
		args = append(args, f.SameAs, f.SameAs)
	}
	if f.Changed {
		b.WriteString(" AND u.changed = 1")
	}
	if q := f.Query; q != "" {
		switch f.In {
		case "key":
			b.WriteString(" AND instr(u.key, ?) > 0")
			args = append(args, q)
		case "context":
			b.WriteString(" AND instr(u.context, ?) > 0")
			args = append(args, q)
		default:
			cols := []string{"source", "target"}
			switch f.In {
			case "source":
				cols = cols[:1]
			case "target":
				cols = cols[1:]
			}
			if utf8.RuneCountInString(q) >= 3 {
				// Trigram FTS: case-insensitive (Unicode-aware) substring match.
				b.WriteString(" AND u.id IN (SELECT rowid FROM units_fts WHERE units_fts MATCH ?)")
				args = append(args, "{"+strings.Join(cols, " ")+"} : "+ftsPhrase(q))
			} else {
				// Too short for trigrams: plain, case-sensitive scan.
				var ors []string
				for _, c := range cols {
					ors = append(ors, "instr(u."+c+", ?) > 0")
					args = append(args, q)
				}
				b.WriteString(" AND (" + strings.Join(ors, " OR ") + ")")
			}
		}
	}
	return b.String(), args
}

func stateSet(ss []model.State) int {
	n := 0
	for _, s := range ss {
		n |= 1 << s
	}
	return n
}

func ftsPhrase(q string) string { return `"` + strings.ReplaceAll(q, `"`, `""`) + `"` }

// Row is a unit as shown in a list: texts are cut to a preview.
type Row struct {
	ID        int64         `json:"id"`
	FileID    int64         `json:"file"`
	Ord       int64         `json:"ord"`
	Key       string        `json:"key"`
	Source    []model.Piece `json:"src"`
	Target    []model.Piece `json:"tgt"` // null = untranslated
	SourceLen int           `json:"srcLen"`
	TargetLen int           `json:"tgtLen"`
	Truncated bool          `json:"trunc,omitempty"`
	State     model.State   `json:"state"`
	QA        qa.Check      `json:"qa"`
	Changed   bool          `json:"changed,omitempty"`
	UpdatedAt int64         `json:"updatedAt"`
	Context   string        `json:"context,omitempty"` // cut to ContextRunes
}

// ContextRunes is how much of a unit's context a list row carries.
const ContextRunes = 120

// PreviewRunes is how much of each text a list row carries.
const PreviewRunes = 400

// List returns up to limit units in the given order, starting after the
// cursor (nil = from the start). next is nil on the last page.
func (s *Store) List(ctx context.Context, f Filter, sort Sort, after *Cursor, limit int) (out []Row, next *Cursor, err error) {
	where, args := f.where()
	a, b, orderBy, desc := sort.order()
	if after != nil {
		op := ">"
		if desc {
			op = "<"
		}
		if sort == SortFile {
			where += " AND (" + a + ", " + b + ") " + op + " (?, ?)"
			args = append(args, after.A, after.B)
		} else {
			// SQLite ignores a row-value range on an expression index
			// (length(source)); "a >= x AND (a > x OR b > y)" seeks instead.
			where += " AND " + a + " " + op + "= ? AND (" + a + " " + op + " ? OR " + b + " " + op + " ?)"
			args = append(args, after.A, after.A, after.B)
		}
	}
	args = append([]any{ContextRunes}, append(args, limit)...)
	rows, err := s.R.QueryContext(ctx, `SELECT u.id, u.file_id, u.ord, u.key, u.source, u.source_pieces,
			u.target, u.target_pieces, u.state, u.qa, u.changed, u.updated_at, substr(u.context, 1, ?)
		FROM units u WHERE `+where+` ORDER BY `+orderBy+` LIMIT ?`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out = make([]Row, 0, limit)
	for rows.Next() {
		var r Row
		var src string
		var tgt sql.NullString
		var srcBlob, tgtBlob []byte
		if err := rows.Scan(&r.ID, &r.FileID, &r.Ord, &r.Key, &src, &srcBlob, &tgt, &tgtBlob, &r.State, &r.QA, &r.Changed, &r.UpdatedAt, &r.Context); err != nil {
			return nil, nil, err
		}
		var t1, t2 bool
		if r.Source, r.SourceLen, t1, err = preview(srcBlob, src); err != nil {
			return nil, nil, err
		}
		if tgt.Valid {
			if r.Target, r.TargetLen, t2, err = preview(tgtBlob, tgt.String); err != nil {
				return nil, nil, err
			}
		}
		r.Truncated = t1 || t2
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) == limit {
		c := sort.cursorOf(out[len(out)-1])
		next = &c
	}
	return out, next, nil
}

func preview(blob []byte, plain string) ([]model.Piece, int, bool, error) {
	n := utf8.RuneCountInString(plain)
	if n <= PreviewRunes {
		ps, err := model.DecodePieces(blob, plain)
		return ps, n, false, err
	}
	ps, err := model.DecodePieces(blob, plain)
	if err != nil {
		return nil, n, false, err
	}
	budget := PreviewRunes
	out := make([]model.Piece, 0, 4)
	for _, p := range ps {
		l := utf8.RuneCountInString(p.Text)
		if l > budget {
			if p.Code {
				break
			}
			i := 0
			for k := 0; k < budget; k++ {
				_, sz := utf8.DecodeRuneInString(p.Text[i:])
				i += sz
			}
			out = append(out, model.Piece{Text: p.Text[:i] + "…"})
			break
		}
		out = append(out, p)
		budget -= l
	}
	return out, n, true, nil
}

// Count returns how many units match f. Filters on location and state alone
// are answered from the per-file counters without touching units.
func (s *Store) Count(ctx context.Context, f Filter) (int64, error) {
	if f.Query == "" && f.QA == 0 && !f.Errors && !f.Changed && f.SameAs == 0 {
		return s.countFromFiles(ctx, f)
	}
	where, args := f.where()
	var n int64
	err := s.R.QueryRowContext(ctx, "SELECT count(*) FROM units u WHERE "+where, args...).Scan(&n)
	return n, err
}

func (s *Store) countFromFiles(ctx context.Context, f Filter) (int64, error) {
	cols := map[model.State]string{
		model.Untranslated: "total - n_mt - n_translated - n_approved",
		model.MT:           "n_mt", model.Translated: "n_translated", model.Approved: "n_approved",
	}
	var terms []string
	states := stateSet(f.States)
	for st := model.Untranslated; st <= model.Approved; st++ {
		if states == 0 || states&(1<<st) != 0 {
			terms = append(terms, cols[st])
		}
	}
	q := "SELECT coalesce(sum(" + strings.Join(terms, " + ") + "), 0) FROM files WHERE project_id = ?"
	args := []any{f.ProjectID}
	if f.FileID != 0 {
		q += " AND id = ?"
		args = append(args, f.FileID)
	} else if f.Dir != "" {
		q += " AND (dir = ? OR substr(dir, 1, ?) = ?)"
		args = append(args, f.Dir, len(f.Dir)+1, f.Dir+"/")
	}
	var n int64
	err := s.R.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// HistoryEntry is one recorded change.
type HistoryEntry struct {
	Target *string     `json:"target"`
	State  model.State `json:"state"`
	Origin string      `json:"origin"`
	At     int64       `json:"at"`
}

// Unit is a full unit.
type Unit struct {
	ID        int64          `json:"id"`
	ProjectID int64          `json:"project"`
	FileID    int64          `json:"file"`
	FilePath  string         `json:"path"`
	Ord       int64          `json:"ord"`
	Key       string         `json:"key"`
	Context   string         `json:"context"`
	Source    []model.Piece  `json:"src"`
	Target    []model.Piece  `json:"tgt"`
	State     model.State    `json:"state"`
	QA        qa.Check       `json:"qa"`
	Changed   bool           `json:"changed"`
	UpdatedAt int64          `json:"updatedAt"`
	History   []HistoryEntry `json:"history,omitempty"`
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getUnit(ctx context.Context, q queryer, id int64) (*Unit, error) {
	var u Unit
	var src string
	var tgt sql.NullString
	var srcBlob, tgtBlob []byte
	err := q.QueryRowContext(ctx, `SELECT u.id, u.project_id, u.file_id, f.path, u.ord, u.key, u.context,
			u.source, u.source_pieces, u.target, u.target_pieces, u.state, u.qa, u.changed, u.updated_at
		FROM units u JOIN files f ON f.id = u.file_id WHERE u.id = ?`, id).
		Scan(&u.ID, &u.ProjectID, &u.FileID, &u.FilePath, &u.Ord, &u.Key, &u.Context,
			&src, &srcBlob, &tgt, &tgtBlob, &u.State, &u.QA, &u.Changed, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.Source, err = model.DecodePieces(srcBlob, src); err != nil {
		return nil, err
	}
	if tgt.Valid {
		if u.Target, err = model.DecodePieces(tgtBlob, tgt.String); err != nil {
			return nil, err
		}
	}
	return &u, nil
}

// Unit returns a unit with its history (newest first).
func (s *Store) Unit(ctx context.Context, id int64, withHistory bool) (*Unit, error) {
	u, err := getUnit(ctx, s.R, id)
	if err != nil || !withHistory {
		return u, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT target, state, origin, at FROM history
		WHERE unit_id = ? ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryEntry
		var t sql.NullString
		if err := rows.Scan(&t, &h.State, &h.Origin, &h.At); err != nil {
			return nil, err
		}
		if t.Valid {
			h.Target = &t.String
		}
		u.History = append(u.History, h)
	}
	return u, rows.Err()
}

// Save is a change to one unit.
type Save struct {
	Target *string     // nil = clear the translation
	State  model.State // ignored when Target is nil (becomes Untranslated)
	Origin string      // edit | mt | bulk
	Force  bool        // store even if the placeholders don't match
	// IfBelow, when set, skips the save (ErrSkipped) if the unit's current
	// state is IfBelow or higher — batch MT uses it so a unit a human edited
	// meanwhile is never overwritten.
	IfBelow model.State
}

// ErrSkipped is returned by SaveUnit when Save.IfBelow rejected the save.
var ErrSkipped = errors.New("skipped: unit already at a higher state")

// SaveUnit validates and stores a translation. A placeholder mismatch is
// returned as *model.CodeMismatch unless Force is set.
func (s *Store) SaveUnit(ctx context.Context, id int64, sv Save, runner *qa.Runner) (*Unit, error) {
	var out *Unit
	err := s.Write(ctx, func(tx *sql.Tx) error {
		u, err := getUnit(ctx, tx, id)
		if err != nil {
			return err
		}
		if sv.IfBelow > 0 && u.State >= sv.IfBelow {
			return ErrSkipped
		}
		var pieces []model.Piece
		state := sv.State
		if sv.Target == nil {
			state = model.Untranslated
		} else {
			pieces, err = model.SplitTarget(*sv.Target, u.Source)
			if err != nil && !sv.Force {
				return err
			}
			if state == model.Untranslated {
				state = model.Translated
			}
		}
		oldPlain := (*string)(nil)
		if u.Target != nil {
			p := model.Plain(u.Target)
			oldPlain = &p
		}
		if eqStr(oldPlain, sv.Target) && u.State == state {
			out = u
			return nil
		}
		now := time.Now().Unix()
		// Keep the pre-edit value the first time a unit is touched, so the
		// imported translation is never lost from history.
		if _, err := tx.ExecContext(ctx, `INSERT INTO history (unit_id, target, state, origin, at)
			SELECT ?, ?, ?, 'import', ? WHERE NOT EXISTS (SELECT 1 FROM history WHERE unit_id = ?)`,
			id, nullStr(oldPlain), u.State, u.UpdatedAt, id); err != nil {
			return err
		}
		mask := runner.Mask(qa.Unit{Source: u.Source, Target: pieces, State: state})
		if _, err := tx.ExecContext(ctx, `UPDATE units SET target = ?, target_pieces = ?, state = ?, qa = ?,
			changed = 0, updated_at = ? WHERE id = ?`,
			nullStr(sv.Target), model.EncodePieces(pieces), state, mask, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history (unit_id, target, state, origin, at) VALUES (?, ?, ?, ?, ?)`,
			id, nullStr(sv.Target), state, sv.Origin, now); err != nil {
			return err
		}
		u.Target, u.State, u.QA, u.Changed, u.UpdatedAt = pieces, state, mask, false, now
		out = u
		return nil
	})
	return out, err
}

func eqStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func nullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// SetState moves every unit matching f (that has a translation) to state.
// With noErrors, units with a QA error are skipped. Returns the count.
func (s *Store) SetState(ctx context.Context, f Filter, state model.State, noErrors bool) (int64, error) {
	where, args := f.where()
	where += " AND u.target IS NOT NULL AND u.state <> ?"
	args = append(args, state)
	if noErrors {
		where += " AND (u.qa & " + errMask + ") = 0"
	}
	var n int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		// History first (the filter still sees the old state): the imported
		// baseline for never-touched units, then the bulk change itself.
		if _, err := tx.ExecContext(ctx, `INSERT INTO history (unit_id, target, state, origin, at)
			SELECT u.id, u.target, u.state, 'import', u.updated_at FROM units u WHERE `+where+`
			AND NOT EXISTS (SELECT 1 FROM history h WHERE h.unit_id = u.id)`, args...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history (unit_id, target, state, origin, at)
			SELECT u.id, u.target, ?, 'bulk', ? FROM units u WHERE `+where,
			append([]any{state, now}, args...)...); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE units SET state = ?, updated_at = ? WHERE id IN (SELECT u.id FROM units u WHERE "+where+")",
			append([]any{state, now}, args...)...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// RecheckQA recomputes the QA mask of every unit of a project and returns
// how many masks changed. progress, if set, is called with (done, total).
func (s *Store) RecheckQA(ctx context.Context, projectID int64, runner *qa.Runner, progress func(done, total int64)) (int64, error) {
	var total int64
	if err := s.R.QueryRowContext(ctx, "SELECT count(*) FROM units WHERE project_id = ?", projectID).Scan(&total); err != nil {
		return 0, err
	}
	type upd struct {
		id   int64
		mask qa.Check
	}
	var changed int64 // stays 0 until the final write
	var pending []upd
	var after int64
	var done int64
	for {
		rows, err := s.R.QueryContext(ctx, `SELECT id, source, source_pieces, target, target_pieces, state, qa
			FROM units WHERE project_id = ? AND id > ? ORDER BY id LIMIT 5000`, projectID, after)
		if err != nil {
			return changed, err
		}
		// Decode the page, then run the checks on all cores: with a glossary
		// they stem every word, which dominates the recheck.
		type item struct {
			id  int64
			u   qa.Unit
			old qa.Check
		}
		var items []item
		for rows.Next() {
			var it item
			var src string
			var tgt sql.NullString
			var sb, tb []byte
			if err := rows.Scan(&it.id, &src, &sb, &tgt, &tb, &it.u.State, &it.old); err != nil {
				rows.Close()
				return changed, err
			}
			if it.u.Source, err = model.DecodePieces(sb, src); err != nil {
				rows.Close()
				return changed, err
			}
			if tgt.Valid {
				if it.u.Target, err = model.DecodePieces(tb, tgt.String); err != nil {
					rows.Close()
					return changed, err
				}
			}
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return changed, err
		}
		n := len(items)
		if n > 0 {
			after = items[n-1].id
		}
		masks := make([]qa.Check, n)
		workers := min(runtime.NumCPU(), max(n/256, 1))
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := w; i < n; i += workers {
					masks[i] = runner.Mask(items[i].u)
				}
			}()
		}
		wg.Wait()
		var batch []upd
		for i, it := range items {
			if masks[i] != it.old {
				batch = append(batch, upd{it.id, masks[i]})
			}
		}
		pending = append(pending, batch...)
		done += int64(n)
		if progress != nil {
			progress(done, total)
		}
		if n == 0 {
			break
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	// One transaction for all changes. Many changes (a glossary or settings
	// edit) skip the per-row counter trigger and recount the files once.
	bulk := len(pending) > 2000
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if bulk {
			if _, err := tx.ExecContext(ctx, "DROP TRIGGER units_au_cnt"); err != nil {
				return err
			}
		}
		stmt, err := tx.PrepareContext(ctx, "UPDATE units SET qa = ? WHERE id = ?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range pending {
			if _, err := stmt.ExecContext(ctx, u.mask, u.id); err != nil {
				return err
			}
		}
		if !bulk {
			return nil
		}
		if err := RecountFiles(ctx, tx, projectID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, counterTriggerDDL)
		return err
	})
	if err != nil {
		return 0, err
	}
	return int64(len(pending)), nil
}
