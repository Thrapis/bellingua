package api

import (
	"context"
	"testing"
	"time"
)

// Update sets stage, message, counters and speed in one step.
func TestProgressUpdate(t *testing.T) {
	jm := NewJobs(context.Background())
	release := make(chan struct{})
	reported := make(chan struct{})
	j := jm.Start("backup", 0, func(ctx context.Context, p Progress) (any, error) {
		p.Update("upload", "Uploading", 250, 1000, 4096)
		close(reported)
		<-release
		return nil, nil
	})
	<-reported
	got, ok := jm.Get(j.ID)
	if !ok || got.Stage != "upload" || got.Message != "Uploading" || got.Done != 250 || got.Total != 1000 || got.Speed != 4096 {
		t.Errorf("job = %+v", got)
	}
	close(release)
	for i := 0; i < 100; i++ {
		if g, _ := jm.Get(j.ID); g.Status == "done" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("job never finished")
}
