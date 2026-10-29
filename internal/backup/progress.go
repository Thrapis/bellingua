package backup

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Progress says where a backup run is. Fraction (0..1) is within Stage:
// "snapshot" (copy, drop the search index, compress) or "upload" (one target
// after the other). Speed is bytes per second, 0 when unknown.
type Progress struct {
	Stage    string
	Message  string
	Fraction float64
	Speed    int64
}

// ProgressFunc receives progress; a nil one is fine and means "do not report".
type ProgressFunc func(Progress)

func (f ProgressFunc) send(p Progress) {
	if f != nil {
		p.Fraction = min(max(p.Fraction, 0), 1)
		f(p)
	}
}

// meter turns a growing byte count into a smoothed bytes-per-second rate.
type meter struct {
	t time.Time
	n int64
	v float64
}

func (m *meter) rate(n int64) int64 {
	now := time.Now()
	if m.t.IsZero() {
		m.t, m.n = now, n
		return 0
	}
	if dt := now.Sub(m.t).Seconds(); dt >= 0.25 {
		inst := float64(n-m.n) / dt
		if m.v == 0 {
			m.v = inst
		} else {
			m.v = 0.5*m.v + 0.5*inst
		}
		m.t, m.n = now, n
	}
	return int64(m.v)
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// countingReader reports how much of r has been read, at most every 150 ms.
// Len is the unread size, which the data server target uses as Content-Length.
type countingReader struct {
	r     io.Reader
	size  int64
	n     int64
	last  time.Time
	onNew func(n int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if now := time.Now(); now.Sub(c.last) >= 150*time.Millisecond || err == io.EOF {
		c.last = now
		c.onNew(c.n)
	}
	return n, err
}

func (c *countingReader) Len() int64 { return c.size - c.n }

// targetLabel makes a target's Name readable: "data:host/app" becomes
// "Data server host/app".
func targetLabel(name string) string {
	kind, rest, ok := strings.Cut(name, ":")
	if !ok {
		return name
	}
	switch kind {
	case "dir":
		return "Folder " + rest
	case "sftp":
		return "SFTP " + rest
	case "data":
		return "Data server " + rest
	}
	return name
}
