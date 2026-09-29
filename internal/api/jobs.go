package api

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Job is a long-running operation (import, export, MT batch, QA recheck,
// backup) whose progress the UI follows over SSE.
type Job struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Project  int64     `json:"project,omitempty"`
	Status   string    `json:"status"` // running | done | failed | canceled
	Done     int64     `json:"done"`
	Total    int64     `json:"total"`
	Message  string    `json:"message,omitempty"`
	Error    string    `json:"error,omitempty"`
	Result   any       `json:"result,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`

	cancel context.CancelFunc
}

// Progress is handed to the job body to report progress.
type Progress struct {
	j  *Job
	jm *Jobs
}

// Set updates the counters.
func (p Progress) Set(done, total int64) {
	p.jm.mu.Lock()
	p.j.Done, p.j.Total = done, total
	p.jm.mu.Unlock()
	p.jm.bump()
}

// Say updates the message.
func (p Progress) Say(format string, args ...any) {
	p.jm.mu.Lock()
	p.j.Message = fmt.Sprintf(format, args...)
	p.jm.mu.Unlock()
	p.jm.bump()
}

// Jobs tracks jobs in memory.
type Jobs struct {
	mu   sync.Mutex
	jobs map[string]*Job
	seq  atomic.Int64
	ver  atomic.Int64 // bumps on any change; SSE loops watch it
	base context.Context
}

// NewJobs creates a registry whose jobs are cancelled when base ends.
func NewJobs(base context.Context) *Jobs {
	return &Jobs{jobs: map[string]*Job{}, base: base}
}

func (jm *Jobs) bump() { jm.ver.Add(1) }

// Start runs fn in the background and returns the job snapshot.
func (jm *Jobs) Start(kind string, project int64, fn func(ctx context.Context, p Progress) (any, error)) Job {
	ctx, cancel := context.WithCancel(jm.base)
	j := &Job{
		ID: fmt.Sprintf("%s-%d", kind, jm.seq.Add(1)), Kind: kind, Project: project,
		Status: "running", Started: time.Now(), cancel: cancel,
	}
	jm.mu.Lock()
	jm.jobs[j.ID] = j
	jm.prune()
	snap := *j
	jm.mu.Unlock()
	jm.bump()

	go func() {
		defer cancel()
		res, err := fn(ctx, Progress{j, jm})
		jm.mu.Lock()
		j.Finished, j.Result = time.Now(), res
		switch {
		case ctx.Err() != nil && err != nil:
			j.Status, j.Error = "canceled", "canceled"
		case err != nil:
			j.Status, j.Error = "failed", err.Error()
		default:
			j.Status = "done"
		}
		jm.mu.Unlock()
		jm.bump()
	}()
	return snap
}

// prune drops finished jobs beyond the newest 50. Caller holds mu.
func (jm *Jobs) prune() {
	if len(jm.jobs) <= 50 {
		return
	}
	var fin []*Job
	for _, j := range jm.jobs {
		if j.Status != "running" {
			fin = append(fin, j)
		}
	}
	sort.Slice(fin, func(a, b int) bool { return fin[a].Started.Before(fin[b].Started) })
	for i := 0; i < len(fin) && len(jm.jobs) > 50; i++ {
		delete(jm.jobs, fin[i].ID)
	}
}

// Get returns a snapshot.
func (jm *Jobs) Get(id string) (Job, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	j, ok := jm.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns snapshots, newest first.
func (jm *Jobs) List() []Job {
	jm.mu.Lock()
	out := make([]Job, 0, len(jm.jobs))
	for _, j := range jm.jobs {
		out = append(out, *j)
	}
	jm.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out
}

// Cancel stops a running job.
func (jm *Jobs) Cancel(id string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	j, ok := jm.jobs[id]
	if ok && j.Status == "running" {
		j.cancel()
	}
	return ok
}

// Running reports whether a job of kind for project is in progress.
func (jm *Jobs) Running(kind string, project int64) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, j := range jm.jobs {
		if j.Status == "running" && j.Kind == kind && j.Project == project {
			return true
		}
	}
	return false
}
