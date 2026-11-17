package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Every list order must be served by its index, not by a sort of the project.
func TestListOrdersUseIndexes(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for sort, index := range map[Sort]string{
		SortFile:        "",
		SortUpdated:     "units_updated",
		SortOldest:      "units_updated",
		SortShort:       "units_len",
		SortTargetShort: "units_tlen",
		SortTargetLong:  "units_tlen",
		SortStatus:      "units_status",
	} {
		where, args := Filter{ProjectID: 1}.where()
		where += sort.where()
		_, _, orderBy, _ := sort.order()
		rows, err := st.R.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT u.id FROM units u WHERE `+where+` ORDER BY `+orderBy+` LIMIT 50`, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if strings.Contains(plan, "TEMP B-TREE") || (index != "" && !strings.Contains(plan, index)) {
			t.Errorf("sort %q (want %s): plan = %s", sort, index, plan)
		}
	}
}

// A pattern with a literal narrows through the trigram index; one without
// has to scan, and must not pretend otherwise.
func TestRegexUsesTrigramIndexForLiterals(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	plan := func(f Filter) string {
		where, args := f.where()
		rows, err := st.R.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT u.id FROM units u WHERE `+where+` ORDER BY u.project_id, u.file_id, u.ord LIMIT 50`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			b.WriteString(detail + "\n")
		}
		t.Logf("%+v\n%s", f, b.String())
		return b.String()
	}
	if p := plan(Filter{ProjectID: 1, Query: `Орбит\p{L}*\b`, In: "source", Regex: true}); !strings.Contains(p, "units_fts") {
		t.Errorf("literal pattern does not use the trigram index:\n%s", p)
	}
	if p := plan(Filter{ProjectID: 1, Query: "Орбита", In: "source", NotQuery: "Арбіта", NotIn: "target"}); !strings.Contains(p, "units_fts") {
		t.Errorf("query with notQuery lost the trigram index:\n%s", p)
	}
	if p := plan(Filter{ProjectID: 1, States: nil, Query: `ў\b`, In: "target", Regex: true}); strings.Contains(p, "units_fts") {
		t.Errorf("a pattern without a literal used the index:\n%s", p)
	}
}
