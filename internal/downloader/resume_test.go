package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// rangedServer serves data with byte-range support and records how many body
// bytes it handed out. cancelAt (when > 0) cancels the download once that many
// bytes have been served, simulating a pause mid-transfer.
type rangedServer struct {
	data     []byte
	served   atomic.Int64
	mu       sync.Mutex
	requests []string
	cancelAt atomic.Int64
	cancel   context.CancelFunc
	once     sync.Once
}

func (s *rangedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.Header.Get("Range"))
	s.mu.Unlock()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"v1"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="file.bin"`)

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprint(len(s.data)))
		return
	}

	start, end := int64(0), int64(len(s.data))-1
	if rng := r.Header.Get("Range"); rng != "" {
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		if start < 0 || end >= int64(len(s.data)) || start > end {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.data)))
		w.WriteHeader(http.StatusPartialContent)
	}

	n := end - start + 1
	buf := make([]byte, 16<<10)
	for off := int64(0); off < n; off += int64(len(buf)) {
		piece := n - off
		if piece > int64(len(buf)) {
			piece = int64(len(buf))
		}
		if _, err := w.Write(s.data[start+off : start+off+piece]); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		got := s.served.Add(piece)
		if limit := s.cancelAt.Load(); limit > 0 && got >= limit && s.cancel != nil {
			s.once.Do(s.cancel)
		}
	}
}

func TestResumeContinuesInsideChunks(t *testing.T) {
	const size = 4 << 20
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	sum := sha256.Sum256(data)

	srv := &rangedServer{data: data}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	ctx, cancel := context.WithCancel(context.Background())
	srv.cancelAt.Store(size / 2)
	srv.cancel = cancel
	opts := Options{
		URL:     ts.URL + "/file.bin",
		Output:  out,
		Conns:   4,
		Retries: 0,
		Quiet:   true,
	}
	err := Run(ctx, opts)
	cancel()
	if err == nil {
		t.Fatal("expected the first run to be cancelled, it completed")
	}

	statePath := out + ".part.zdl.json"
	st, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("load state after pause: %v", err)
	}
	before := st.ProgressBytes()
	t.Logf("paused at %d/%d bytes (completed chunks %d)", before, size, st.CompletedBytes())
	if before <= 0 || before >= size {
		t.Fatalf("unexpected paused progress %d/%d", before, size)
	}
	partialChunks := 0
	for i := range st.Done {
		if !st.Done[i] && st.ChunkBytes[i] > 0 {
			partialChunks++
		}
	}
	if partialChunks == 0 {
		t.Fatal("no mid-chunk progress was saved; resume would lose in-flight bytes")
	}

	// Resume. The output path must be the same one the paused job used.
	srv.served.Store(0)
	srv.cancelAt.Store(0)
	opts.Output = st.Output
	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("resume: %v", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if len(got) != size {
		t.Fatalf("output size %d, want %d", len(got), size)
	}
	gotSum := sha256.Sum256(got)
	if hex.EncodeToString(gotSum[:]) != hex.EncodeToString(sum[:]) {
		t.Fatal("resumed output checksum mismatch")
	}

	// Resuming must not re-fetch bytes that were already on disk. Allow a
	// small margin for the probe and the head/tail validators.
	after := srv.served.Load()
	remaining := int64(size) - before
	t.Logf("resume served %d bytes; remaining was %d (whole file is %d)", after, remaining, size)
	if after < remaining {
		t.Fatalf("resume served %d bytes, less than the %d missing", after, remaining)
	}
	if after > remaining+(1<<20) {
		t.Fatalf("resume re-fetched data: served %d bytes for %d missing", after, remaining)
	}
	if !strings.HasSuffix(st.Output, "file.bin") {
		t.Fatalf("unexpected output path %q", st.Output)
	}
}

func TestProgressBytesIncludesPartialChunks(t *testing.T) {
	st := &State{
		Size:       3 << 20,
		RangeOK:    true,
		ChunkSize:  1 << 20,
		Done:       []bool{true, false, false},
		ChunkBytes: []int64{1 << 20, 300 << 10, 0},
	}
	want := int64((1 << 20) + (300 << 10))
	if got := st.ProgressBytes(); got != want {
		t.Fatalf("ProgressBytes = %d, want %d", got, want)
	}
	if got := st.CompletedBytes(); got != 1<<20 {
		t.Fatalf("CompletedBytes = %d, want %d", got, int64(1<<20))
	}
	// A legacy state without ChunkBytes still reports completed chunks.
	st.ChunkBytes = nil
	if got := st.ProgressBytes(); got != 1<<20 {
		t.Fatalf("legacy ProgressBytes = %d, want %d", got, int64(1<<20))
	}
}
