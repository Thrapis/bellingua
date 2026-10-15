package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// Migration 7 on a COPY of the real corpus: BELLINGUA_MIGRATE_DB=copy.db
// go test -run TestMigrateRealCopy -v ./internal/store (never the real file).
func TestMigrateRealCopy(t *testing.T) {
	path := os.Getenv("BELLINGUA_MIGRATE_DB")
	if path == "" {
		t.Skip("BELLINGUA_MIGRATE_DB not set")
	}
	t0 := time.Now()
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n, nb int64
	st.R.QueryRow(`SELECT count(*), count(old_state) FROM history`).Scan(&n, &nb)
	t.Logf("open+migrate %v; history rows %d, with old_state %d", time.Since(t0), n, nb)
	for _, q := range []string{
		`EXPLAIN QUERY PLAN SELECT max(at) FROM history WHERE project_id = 1 AND old_state IS NOT NULL AND at < 9999999999`,
		`EXPLAIN QUERY PLAN SELECT COALESCE(batch, origin || '@' || (at / 3600)), origin, old_state, state, count(*), min(at), max(at) FROM history WHERE project_id = 1 AND old_state IS NOT NULL AND at >= 0 AND at < 9999999999 GROUP BY 1, origin, old_state, state`,
		`EXPLAIN QUERY PLAN SELECT h.id FROM history h JOIN units u ON u.id = h.unit_id JOIN files f ON f.id = u.file_id WHERE h.project_id = 1 AND h.old_state IS NOT NULL AND h.origin IN ('ai') AND (h.at < 9999999999 OR (h.at = 9999999999 AND h.id < 5)) ORDER BY h.at DESC, h.id DESC LIMIT 101`,
	} {
		rows, err := st.R.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a, b, c int
			var d string
			rows.Scan(&a, &b, &c, &d)
			t.Log(d)
		}
		rows.Close()
	}
}

func BenchmarkHistoryDays(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := st.HistoryDays(ctx, pid, 10800, 0, 7); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHistoryList(b *testing.B) {
	st, pid := benchStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := st.HistoryList(ctx, HistoryFilter{ProjectID: pid}, nil, 100); err != nil {
			b.Fatal(err)
		}
	}
}
