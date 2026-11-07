package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JobState is the lifecycle state shown by the UI.
type JobState int

const (
	JobQueued JobState = iota
	JobActive
	JobPaused
	JobDone
	JobFailed
)

func (s JobState) String() string {
	switch s {
	case JobQueued:
		return "queued"
	case JobActive:
		return "active"
	case JobPaused:
		return "paused"
	case JobDone:
		return "done"
	case JobFailed:
		return "failed"
	}
	return "?"
}

// Job is one queued download.
type Job struct {
	ID       string
	URL      string
	Output   string
	Dir      string
	Conns    int
	Retries  int
	Rate     int64
	Headers  Headers
	Insecure bool
	SHA256   string
	Force    bool
	Restart  bool
}

// JobStatus is a point-in-time copy handed to the UI.
type JobStatus struct {
	ID         string
	URL        string
	Title      string
	Output     string
	State      JobState
	Done       int64
	Total      int64
	Speed      float64
	Elapsed    time.Duration
	Left       time.Duration
	Conns      int
	Err        error
	AddedAt    time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

func (s JobStatus) Percent() float64 {
	if s.Total <= 0 {
		return 0
	}
	p := float64(s.Done) / float64(s.Total)
	if p > 1 {
		p = 1
	}
	return p
}

type jobEntry struct {
	job           Job
	st            JobStatus
	cancel        context.CancelFunc
	running       bool
	pausing       bool
	resumePending bool
	deleted       bool
	removeFiles   bool
}

// Engine schedules jobs, enforces the concurrency limit, and tracks state.
// It is safe for concurrent use; the UI polls Snapshot.
type Engine struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	max         int
	seq         int
	closing     bool
	globalRate  int64
	globalLimit *RateLimiter
	entries     []*jobEntry
	byID        map[string]*jobEntry
}

func NewEngine(parent context.Context, maxConcurrent int) *Engine {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	ctx, cancel := context.WithCancel(parent)
	return &Engine{
		ctx:    ctx,
		cancel: cancel,
		max:    maxConcurrent,
		byID:   make(map[string]*jobEntry),
	}
}

// Close cancels every running job and waits for their state to be saved.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closing = true
	e.mu.Unlock()
	e.cancel()
	e.wg.Wait()
}

func (e *Engine) Max() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.max
}

func (e *Engine) SetMax(n int) {
	if n < 1 {
		n = 1
	}
	e.mu.Lock()
	e.max = n
	e.mu.Unlock()
	e.schedule()
}

// SetGlobalRate sets a shared byte-per-second cap across all active jobs.
// 0 means unlimited. The limiter is created lazily on the first start.
func (e *Engine) SetGlobalRate(rate int64) {
	if rate < 0 {
		rate = 0
	}
	e.mu.Lock()
	e.globalRate = rate
	e.globalLimit = nil // force recreation so the new rate applies
	e.mu.Unlock()
	e.schedule()
}

// globalLimiterLocked returns the shared rate limiter, creating it on first use.
func (e *Engine) globalLimiterLocked() *RateLimiter {
	if e.globalRate <= 0 {
		return nil
	}
	if e.globalLimit == nil {
		e.globalLimit = NewRateLimiter(e.globalRate)
	}
	return e.globalLimit
}

// Add queues a job and returns its initial status.
func (e *Engine) Add(j Job) JobStatus {
	if j.Conns < 1 {
		j.Conns = 16
	}
	if j.Conns > 64 {
		j.Conns = 64
	}
	if j.Retries < 0 {
		j.Retries = 0
	}

	e.mu.Lock()
	e.seq++
	if j.ID == "" {
		j.ID = fmt.Sprintf("j%d", e.seq)
	}
	st := JobStatus{
		ID:      j.ID,
		URL:     j.URL,
		Title:   titledJob(j),
		Output:  j.Output,
		State:   JobQueued,
		Conns:   j.Conns,
		AddedAt: time.Now(),
	}
	en := &jobEntry{job: j, st: st}
	e.entries = append(e.entries, en)
	e.byID[j.ID] = en
	e.mu.Unlock()

	e.schedule()
	return st
}

func titledJob(j Job) string {
	if j.Output != "" {
		return filepath.Base(j.Output)
	}
	if name := sanitizeFilename(filenameFromURL(j.URL)); name != "" {
		return name
	}
	return j.URL
}

func (e *Engine) schedule() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return
	}

	active := 0
	for _, en := range e.entries {
		if en.running {
			active++
		}
	}
	for _, en := range e.entries {
		if active >= e.max {
			return
		}
		if en.st.State != JobQueued || en.running {
			continue
		}
		e.startLocked(en)
		active++
	}
}

func (e *Engine) startLocked(en *jobEntry) {
	ctx, cancel := context.WithCancel(e.ctx)
	en.cancel = cancel
	en.running = true
	en.pausing = false
	en.st.State = JobActive
	en.st.StartedAt = time.Now()
	en.st.Err = nil
	en.st.Speed = 0
	en.st.Left = -1
	e.wg.Add(1)
	go e.run(ctx, en)
}
func (e *Engine) run(ctx context.Context, en *jobEntry) {
	defer e.wg.Done()
	job := en.job
	opts := Options{
		URL:      job.URL,
		Output:   job.Output,
		Dir:      job.Dir,
		Conns:    job.Conns,
		Retries:  job.Retries,
		Headers:  job.Headers,
		Insecure: job.Insecure,
		Rate:     job.Rate,
		SHA256:   job.SHA256,
		Force:    job.Force,
		Restart:  job.Restart,
		Quiet:    true,
		Limiter:  e.globalLimiterLocked(),
		OnPlan: func(p Plan) {
			e.mu.Lock()
			// Remember where the bytes actually land (the path may have been
			// uniquified, e.g. "file (1).bin"). Without this a pause/resume
			// would re-derive the original name, miss the .part state, and
			// start over in a new file.
			en.job.Output = p.Output
			en.st.Output = p.Output
			en.st.Title = filepath.Base(p.Output)
			en.st.Total = p.Size
			en.st.Conns = p.Conns
			e.mu.Unlock()
		},
		Progress: func(s Snapshot) {
			e.mu.Lock()
			en.st.Done = s.Done
			en.st.Total = s.Total
			en.st.Speed = s.Speed
			en.st.Elapsed = s.Elapsed
			en.st.Left = s.Left
			en.st.Conns = s.Conns
			e.mu.Unlock()
		},
	}

	err := Run(ctx, opts)

	e.mu.Lock()
	en.running = false
	en.cancel = nil

	if en.deleted {
		rm := en.removeFiles
		out := en.st.Output
		e.mu.Unlock()
		if rm && out != "" {
			cleanupOutput(out)
		}
		e.schedule()
		return
	}
	if en.resumePending {
		en.resumePending = false
		en.st.State = JobQueued
		en.st.Err = nil
		e.mu.Unlock()
		e.schedule()
		return
	}

	switch {
	case err == nil:
		en.st.State = JobDone
		en.st.Done = en.st.Total
		en.st.Speed = 0
		en.st.Left = 0
		en.st.FinishedAt = time.Now()
	case en.pausing || ctx.Err() != nil:
		en.st.State = JobPaused
		en.st.Speed = 0
		en.st.Left = -1
	default:
		en.st.State = JobFailed
		en.st.Err = err
		en.st.Speed = 0
		en.st.Left = -1
		en.st.FinishedAt = time.Now()
	}
	e.mu.Unlock()
	e.schedule()
}

// Pause stops a running job, keeping its .part file and state for resume.
func (e *Engine) Pause(id string) {
	e.mu.Lock()
	en := e.byID[id]
	if en == nil {
		e.mu.Unlock()
		return
	}
	switch en.st.State {
	case JobActive:
		en.pausing = true
		if en.cancel != nil {
			en.cancel()
		}
		en.st.State = JobPaused
		en.st.Speed = 0
		en.st.Left = -1
	case JobQueued:
		en.st.State = JobPaused
	}
	e.mu.Unlock()
}

// Resume continues a paused or failed job from its saved .part.
func (e *Engine) Resume(id string) {
	e.ResumeWith(id, Headers{})
}

// ResumeWith is Resume with freshly resolved headers (for example Zen
// cookies, which are not persisted in the history).
func (e *Engine) ResumeWith(id string, headers Headers) {
	e.mu.Lock()
	en := e.byID[id]
	if en == nil {
		e.mu.Unlock()
		return
	}
	if en.st.State != JobPaused && en.st.State != JobFailed {
		e.mu.Unlock()
		return
	}
	if headers.UserAgent != "" || headers.Referer != "" || headers.Cookie != "" || len(headers.Extra) > 0 {
		en.job.Headers = headers
	}
	// Always resume into the file the job already opened, never a freshly
	// derived (and possibly uniquified) name.
	if en.job.Output == "" && en.st.Output != "" {
		en.job.Output = en.st.Output
	}
	if en.running {
		// Still unwinding from the cancel; restart as soon as it exits.
		en.resumePending = true
		en.st.State = JobQueued
		e.mu.Unlock()
		return
	}
	en.st.State = JobQueued
	en.st.Err = nil
	e.mu.Unlock()
	e.schedule()
}

// ResumeWithURL is Resume with a refreshed URL (for example a renewed Zen
// link). The saved .part state is kept; Run re-probes the new URL and
// validates it against the saved state before continuing.
func (e *Engine) ResumeWithURL(id, url string) error {
	e.mu.Lock()
	en := e.byID[id]
	if en == nil {
		e.mu.Unlock()
		return errors.New("download not found")
	}
	if en.st.State != JobPaused && en.st.State != JobFailed {
		e.mu.Unlock()
		return errors.New("can only resume a paused or failed download")
	}
	if strings.TrimSpace(url) != "" {
		en.job.URL = strings.TrimSpace(url)
		en.st.URL = en.job.URL
	}
	if en.running {
		en.resumePending = true
		en.st.State = JobQueued
		e.mu.Unlock()
		return nil
	}
	en.st.State = JobQueued
	en.st.Err = nil
	e.mu.Unlock()
	e.schedule()
	return nil
}

// Remove drops a job from the list. If removeFiles is true, the final file,
// part, and state are deleted once any active transfer has stopped.
func (e *Engine) Remove(id string, removeFiles bool) {
	e.mu.Lock()
	en := e.byID[id]
	if en == nil {
		e.mu.Unlock()
		return
	}
	delete(e.byID, id)
	for i, x := range e.entries {
		if x == en {
			e.entries = append(e.entries[:i], e.entries[i+1:]...)
			break
		}
	}
	active := en.running
	en.deleted = true
	en.removeFiles = removeFiles
	if active && en.cancel != nil {
		en.cancel()
	}
	out := en.st.Output
	e.mu.Unlock()

	if !active && removeFiles && out != "" {
		cleanupOutput(out)
	}
	e.schedule()
}

// Snapshot returns a copy of all job statuses in list order.
func (e *Engine) Snapshot() []JobStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]JobStatus, 0, len(e.entries))
	for _, en := range e.entries {
		out = append(out, en.st)
	}
	return out
}

// TotalSpeed sums the current speed of all active jobs.
func (e *Engine) TotalSpeed() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var total float64
	for _, en := range e.entries {
		total += en.st.Speed
	}
	return total
}

func cleanupOutput(out string) {
	for _, p := range []string{out, out + ".part", out + ".part.zdl.json", out + ".part.zdl.json.tmp"} {
		_ = os.Remove(p)
	}
}
