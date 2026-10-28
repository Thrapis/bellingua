package backup

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A run reports a snapshot stage and then an upload stage; within a stage the
// fraction never goes back and each stage ends at 1.
func TestRunReportsProgress(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	addUnit(t, st, "first", "Привет, мир")

	var got []Progress
	_, err := RunProgress(ctx, st.Path, []Target{LocalDir(t.TempDir())}, 5, func(p Progress) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}

	last := map[string]float64{}
	var order []string
	for _, p := range got {
		if p.Fraction < 0 || p.Fraction > 1 {
			t.Errorf("fraction %v out of range: %+v", p.Fraction, p)
		}
		if p.Message == "" {
			t.Errorf("empty message: %+v", p)
		}
		if prev, ok := last[p.Stage]; ok && p.Fraction < prev {
			t.Errorf("%s went back: %v -> %v (%s)", p.Stage, prev, p.Fraction, p.Message)
		}
		if _, ok := last[p.Stage]; !ok {
			order = append(order, p.Stage)
		}
		last[p.Stage] = p.Fraction
	}
	if strings.Join(order, ",") != "snapshot,upload" {
		t.Fatalf("stages = %v", order)
	}
	if last["snapshot"] != 1 || last["upload"] != 1 {
		t.Errorf("stages must end at 1: %v", last)
	}
}

// Progress reporting wraps the upload body; the data server must still get a
// Content-Length (no chunked upload).
func TestDataServerKeepsContentLengthWithProgress(t *testing.T) {
	ctx := context.Background()
	st := newDB(t, "first")
	defer st.Close()
	addUnit(t, st, "first", "Привет, мир")
	f, target := newDataServer(t, "")
	res, err := RunProgress(ctx, st.Path, []Target{target}, 5, func(Progress) {})
	if err != nil {
		t.Fatal(err)
	}
	if f.putLen != res.Size || res.Size == 0 {
		t.Errorf("Content-Length = %d, snapshot is %d bytes", f.putLen, res.Size)
	}
}

func TestMeterSpeed(t *testing.T) {
	var m meter
	if v := m.rate(0); v != 0 {
		t.Errorf("first sample = %d", v)
	}
	m.t = m.t.Add(-time.Second) // pretend the first sample was a second ago
	if v := m.rate(1 << 20); v < 900<<10 || v > 1100<<10 {
		t.Errorf("1 MiB in ~1 s = %d B/s", v)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 29: "1.5 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTargetLabel(t *testing.T) {
	for in, want := range map[string]string{
		"data:1.2.3.4:9696/bellingua": "Data server 1.2.3.4:9696/bellingua",
		"sftp:u@vps:/home/u/bl":       "SFTP u@vps:/home/u/bl",
		"dir:D:/Backups":              "Folder D:/Backups",
		"something-else":              "something-else",
	} {
		if got := targetLabel(in); got != want {
			t.Errorf("targetLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
