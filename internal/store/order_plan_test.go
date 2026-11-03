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
