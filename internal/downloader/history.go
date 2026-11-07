package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StoredJob is one persisted history entry. It holds enough of the original
// Job to resume it after a restart, plus the last observed status.
type StoredJob struct {
	ID         string        `json:"id"`
	URL        string        `json:"url"`
	Title      string        `json:"title,omitempty"`
	Output     string        `json:"output,omitempty"`
	Dir        string        `json:"dir,omitempty"`
	State      string        `json:"state"`
	Done       int64         `json:"done,omitempty"`
	Total      int64         `json:"total,omitempty"`
	Speed      float64       `json:"speed,omitempty"`
	Elapsed    time.Duration `json:"elapsed,omitempty"`
	Left       time.Duration `json:"left,omitempty"`
	Conns      int           `json:"connections,omitempty"`
	Retries    int           `json:"retries,omitempty"`
	Rate       int64         `json:"rate,omitempty"`
	Insecure   bool          `json:"insecure,omitempty"`
	SHA256     string        `json:"sha256,omitempty"`
	Err        string        `json:"error,omitempty"`
	AddedAt    time.Time     `json:"added_at,omitempty"`
	FinishedAt time.Time     `json:"finished_at,omitempty"`
}

type historyFile struct {
	Version int         `json:"version"`
	Jobs    []StoredJob `json:"jobs"`
}

// LoadHistory reads the dashboard history. A missing file is not an error.
func LoadHistory(path string) ([]StoredJob, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var hf historyFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, fmt.Errorf("history %s: %w", path, err)
	}
	return hf.Jobs, nil
}

// SaveHistory atomically writes the dashboard history. When maxJobs > 0, only
// the most recent maxJobs finished/failed entries are kept; active, queued, and
// paused entries are always retained.
func SaveHistory(path string, jobs []StoredJob, maxJobs int) error {
	if path == "" {
		return nil
	}
	if maxJobs > 0 {
		jobs = pruneHistory(jobs, maxJobs)
	}
	hf := historyFile{Version: 1, Jobs: jobs}
	// Compact JSON keeps the once-a-second rewrite cheap even when the
	// history is full.
	data, err := json.Marshal(hf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pruneHistory keeps every active/queued/paused entry plus the most recent
// maxJobs finished/failed entries (by FinishedAt, then AddedAt).
func pruneHistory(jobs []StoredJob, maxJobs int) []StoredJob {
	var keep []StoredJob
	var finished []StoredJob
	for _, j := range jobs {
		switch j.State {
		case "queued", "active", "paused":
			keep = append(keep, j)
		default:
			finished = append(finished, j)
		}
	}
	sort.Slice(finished, func(i, k int) bool {
		if !finished[i].FinishedAt.IsZero() && !finished[k].FinishedAt.IsZero() {
			return finished[i].FinishedAt.Before(finished[k].FinishedAt)
		}
		return finished[i].AddedAt.Before(finished[k].AddedAt)
	})
	if len(finished) > maxJobs {
		finished = finished[len(finished)-maxJobs:]
	}
	keep = append(keep, finished...)
	return keep
}

// Records exports the current jobs for persistence.
func (e *Engine) Records() []StoredJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]StoredJob, 0, len(e.entries))
	for _, en := range e.entries {
		st := en.st
		rec := StoredJob{
			ID:         st.ID,
			URL:        st.URL,
			Title:      st.Title,
			Output:     st.Output,
			Dir:        en.job.Dir,
			State:      st.State.String(),
			Done:       st.Done,
			Total:      st.Total,
			Speed:      st.Speed,
			Elapsed:    st.Elapsed,
			Left:       st.Left,
			Conns:      st.Conns,
			Retries:    en.job.Retries,
			Rate:       en.job.Rate,
			Insecure:   en.job.Insecure,
			SHA256:     en.job.SHA256,
			AddedAt:    st.AddedAt,
			FinishedAt: st.FinishedAt,
		}
		if st.Err != nil {
			rec.Err = st.Err.Error()
		}
		out = append(out, rec)
	}
	return out
}

// Restore loads persisted jobs without starting them. Anything that was
// active or queued in a previous session becomes paused so it can be resumed
// deliberately; finished and failed entries keep their state.
func (e *Engine) Restore(jobs []StoredJob) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rec := range jobs {
		job, ok := jobFromRecord(rec)
		if !ok {
			continue
		}
		if _, ok := e.byID[rec.ID]; ok {
			continue
		}
		st, ok := statusFromRecord(rec, true)
		if !ok {
			continue
		}
		en := &jobEntry{job: job, st: st}
		e.entries = append(e.entries, en)
		e.byID[rec.ID] = en
		if n, ok := idNumber(rec.ID); ok && n > e.seq {
			e.seq = n
		}
	}
}

// HistoryStatuses converts persisted records for a read-only viewer. Unlike
// Restore it keeps active/queued states as recorded by the owning process.
func HistoryStatuses(jobs []StoredJob) []JobStatus {
	out := make([]JobStatus, 0, len(jobs))
	for _, rec := range jobs {
		if st, ok := statusFromRecord(rec, false); ok {
			out = append(out, st)
		}
	}
	return out
}

func jobFromRecord(rec StoredJob) (Job, bool) {
	if rec.ID == "" || rec.URL == "" {
		return Job{}, false
	}
	conns := rec.Conns
	if conns < 1 {
		conns = 16
	}
	if conns > 64 {
		conns = 64
	}
	return Job{
		ID:       rec.ID,
		URL:      rec.URL,
		Output:   rec.Output,
		Dir:      rec.Dir,
		Conns:    conns,
		Retries:  rec.Retries,
		Rate:     rec.Rate,
		Insecure: rec.Insecure,
		SHA256:   rec.SHA256,
	}, true
}

func statusFromRecord(rec StoredJob, restore bool) (JobStatus, bool) {
	job, ok := jobFromRecord(rec)
	if !ok {
		return JobStatus{}, false
	}
	title := rec.Title
	if title == "" {
		title = titledJob(job)
	}
	st := JobStatus{
		ID:         rec.ID,
		URL:        rec.URL,
		Title:      title,
		Output:     rec.Output,
		Done:       rec.Done,
		Total:      rec.Total,
		Speed:      rec.Speed,
		Elapsed:    rec.Elapsed,
		Left:       rec.Left,
		Conns:      job.Conns,
		AddedAt:    rec.AddedAt,
		FinishedAt: rec.FinishedAt,
	}
	switch rec.State {
	case "done":
		st.State = JobDone
		st.Speed = 0
		st.Left = 0
		if st.Total > 0 {
			st.Done = st.Total
		}
	case "failed":
		st.State = JobFailed
		st.Speed = 0
		st.Left = -1
		if rec.Err != "" {
			st.Err = errors.New(rec.Err)
		}
	case "active":
		if restore {
			st.State = JobPaused
			st.Speed = 0
			st.Left = -1
		} else {
			st.State = JobActive
			if st.Left == 0 {
				st.Left = -1
			}
		}
	case "queued":
		if restore {
			st.State = JobPaused
		} else {
			st.State = JobQueued
		}
		st.Speed = 0
		st.Left = -1
	default:
		st.State = JobPaused
		st.Speed = 0
		st.Left = -1
	}
	if st.AddedAt.IsZero() {
		st.AddedAt = time.Now()
	}
	return st, true
}

func idNumber(id string) (int, bool) {
	if !strings.HasPrefix(id, "j") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "j"))
	if err != nil {
		return 0, false
	}
	return n, true
}
