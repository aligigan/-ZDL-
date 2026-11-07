package downloader

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.json")
	now := time.Now().Round(time.Second)
	want := []StoredJob{
		{
			ID: "j1", URL: "https://example.com/a.bin", Title: "a.bin",
			Output: "/tmp/a.bin", Dir: "/tmp", State: "done",
			Done: 10, Total: 10, Elapsed: 2 * time.Second,
			Conns: 8, AddedAt: now, FinishedAt: now,
		},
		{
			ID: "j2", URL: "https://example.com/b.bin", State: "active",
			Done: 5, Total: 10, Conns: 4, Rate: 1 << 20,
		},
	}
	if err := SaveHistory(path, want, 0); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	got, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestLoadHistoryMissing(t *testing.T) {
	got, err := LoadHistory(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing history: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d jobs, want 0", len(got))
	}
}

func TestRestoreStates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(ctx, 1)

	e.Restore([]StoredJob{
		{ID: "j4", URL: "https://example.com/done", Title: "done.bin", State: "done", Total: 9, Done: 9},
		{ID: "j5", URL: "https://example.com/failed", Title: "failed.bin", State: "failed", Total: 10, Done: 3, Err: "boom"},
		{ID: "j6", URL: "https://example.com/active", Title: "active.bin", State: "active", Total: 10, Done: 4},
		{ID: "j7", URL: "https://example.com/queued", Title: "queued.bin", State: "queued", Total: 10},
	})

	jobs := e.Snapshot()
	if len(jobs) != 4 {
		t.Fatalf("got %d jobs, want 4", len(jobs))
	}
	byID := make(map[string]JobStatus, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}
	if got := byID["j4"].State; got != JobDone {
		t.Errorf("done entry state = %v", got)
	}
	if j := byID["j5"]; j.State != JobFailed || j.Err == nil {
		t.Errorf("failed entry state = %v err = %v", j.State, j.Err)
	}
	for _, id := range []string{"j6", "j7"} {
		if got := byID[id].State; got != JobPaused {
			t.Errorf("%s state = %v, want paused", id, got)
		}
	}

	e.mu.Lock()
	seq := e.seq
	e.mu.Unlock()
	if seq != 7 {
		t.Errorf("seq = %d, want 7", seq)
	}
	e.Close()
}

func TestRecordsKeepsJobSettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(ctx, 1)
	e.Restore([]StoredJob{{
		ID: "j9", URL: "https://example.com/x", Output: "/tmp/x", Dir: "/tmp",
		State: "paused", Total: 100, Done: 25, Conns: 32, Retries: 7,
		Rate: 8 << 20, Insecure: true, SHA256: "abc",
	}})
	rec := e.Records()
	if len(rec) != 1 {
		t.Fatalf("got %d records", len(rec))
	}
	r := rec[0]
	if r.Conns != 32 || r.Retries != 7 || r.Rate != 8<<20 || !r.Insecure || r.SHA256 != "abc" {
		t.Errorf("settings not preserved: %#v", r)
	}
	if r.State != "paused" || r.Done != 25 {
		t.Errorf("status not preserved: %#v", r)
	}
	e.Close()
}

func TestHistoryStatusesKeepLiveState(t *testing.T) {
	got := HistoryStatuses([]StoredJob{
		{ID: "j1", URL: "https://example.com/a", Title: "a", State: "active", Done: 50, Total: 100, Speed: 1 << 20, Left: 3 * time.Second},
		{ID: "j2", URL: "https://example.com/b", State: "queued", Total: 10},
		{ID: "j3", URL: "https://example.com/c", State: "paused", Done: 5, Total: 10},
		{ID: "j4", URL: "https://example.com/d", State: "done", Done: 10, Total: 10},
	})
	if len(got) != 4 {
		t.Fatalf("got %d statuses, want 4", len(got))
	}
	byID := make(map[string]JobStatus, len(got))
	for _, j := range got {
		byID[j.ID] = j
	}
	if j := byID["j1"]; j.State != JobActive || j.Done != 50 || j.Speed != 1<<20 || j.Left != 3*time.Second {
		t.Errorf("active entry not live: %#v", j)
	}
	if j := byID["j2"]; j.State != JobQueued {
		t.Errorf("queued entry state = %v", j.State)
	}
	if j := byID["j3"]; j.State != JobPaused || j.Done != 5 {
		t.Errorf("paused entry = %v done %d", j.State, j.Done)
	}
	if j := byID["j4"]; j.State != JobDone || j.Left != 0 {
		t.Errorf("done entry = %v left %v", j.State, j.Left)
	}
}
