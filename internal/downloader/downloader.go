package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	URL      string
	Output   string // final output path; empty = derive from Content-Disposition/URL
	Dir      string // used when Output is empty
	Conns    int
	Retries  int
	Headers  Headers
	Insecure bool
	Rate     int64        // bytes per second, 0 = unlimited
	Limiter  *RateLimiter // shared global limiter; when set, overrides Rate
	SHA256   string
	Force    bool
	Restart  bool
	Quiet    bool
	Verbose  bool
	Notify   bool
	Resume   bool // state must already exist
	// Confirm, when set, is called after probing and validation but before any
	// bytes are written or state files are created. Returning false cancels.
	Confirm func(Plan) (bool, error)
	// OnPlan receives the resolved plan even when Confirm is nil. The engine
	// uses it to learn the final filename and size before bytes move.
	OnPlan func(Plan)
	// Progress receives periodic samples. When set together with Quiet, the
	// terminal renderer is disabled and only the callback fires.
	Progress func(Snapshot)
}

// Plan is what the confirmation prompt shows before a transfer starts.
type Plan struct {
	URL          string
	Output       string
	Size         int64
	RangeOK      bool
	ETag         string
	LastModified string
	ContentType  string
	Conns        int
	Chunks       int
	ChunkSize    int64
	Resume       bool
	DoneBytes    int64
	SHA256       string
}

// ErrCancelled is returned when the user declines the confirmation prompt.
var ErrCancelled = errors.New("cancelled by user")

// ResumableError adds the information needed to print a resume command.
type ResumableError struct {
	Err    error
	Output string
	URL    string
	SHA256 string // carried so the resume hint keeps verifying the file
}

func (e *ResumableError) Error() string { return e.Err.Error() }
func (e *ResumableError) Unwrap() error { return e.Err }

func Run(ctx context.Context, opts Options) error {
	if opts.Conns < 1 {
		opts.Conns = 1
	}
	if opts.Conns > 64 {
		return fmt.Errorf("--connections cannot exceed 64")
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.Dir == "" {
		opts.Dir = defaultDownloadDir()
	}

	client := NewClient(opts.Conns, opts.Insecure)
	defer client.CloseIdleConnections()

	var (
		st        *State
		out       string
		part      string
		statePath string
	)

	if opts.Resume {
		if opts.Output == "" {
			return errors.New("resume needs an output file")
		}
		var err error
		out, err = filepath.Abs(opts.Output)
		if err != nil {
			return err
		}
		part = out + ".part"
		statePath = part + ".zdl.json"
		st, err = LoadState(statePath)
		if err != nil {
			return fmt.Errorf("no resumable download at %s: %w", statePath, err)
		}
		if opts.URL == "" {
			opts.URL = st.FinalURL
			if opts.URL == "" {
				opts.URL = st.URL
			}
		}
	}

	if opts.URL == "" {
		return errors.New("no URL")
	}
	if !strings.HasPrefix(opts.URL, "http://") && !strings.HasPrefix(opts.URL, "https://") {
		return fmt.Errorf("unsupported URL scheme: %s", opts.URL)
	}

	probe, err := Probe(ctx, client, opts.URL, opts.Headers)
	if err != nil {
		return err
	}
	if opts.Verbose && !opts.Quiet {
		fmt.Printf("probe: %s  size=%s  range=%v  etag=%s\n",
			probe.Filename, HumanBytes(probe.Size), probe.RangeOK, probe.ETag)
	}

	if out == "" {
		if opts.Output != "" {
			out, err = filepath.Abs(opts.Output)
		} else {
			out = filepath.Join(opts.Dir, sanitizeFilename(probe.Filename))
		}
		if err != nil {
			return err
		}
	}
	if out == "" {
		return errors.New("could not determine output filename")
	}
	part = out + ".part"
	statePath = part + ".zdl.json"

	if opts.Restart {
		os.Remove(part)
		os.Remove(statePath)
		st = nil
	}

	if st == nil && !opts.Resume {
		if s, err := LoadState(statePath); err == nil {
			st = s
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%w (use --restart to discard it)", err)
		}
	}

	resuming := st != nil
	if resuming {
		if st.VerifyFailed && !opts.Force && !opts.Restart {
			return fmt.Errorf("%s failed checksum verification earlier; use --restart to download it again or --force to accept it", out)
		}
		if err := validateResume(ctx, client, st, probe, opts.Headers, opts.Force); err != nil {
			return err
		}
	} else {
		if fileExists(out) && !opts.Force {
			if opts.Output != "" {
				return fmt.Errorf("%s already exists (use --force to overwrite)", out)
			}
			// Derived names are uniquified, so downloading several files with
			// the same basename works without -o gymnastics.
			out = uniquePath(out)
			part = out + ".part"
			statePath = part + ".zdl.json"
		}
	}

	if opts.Confirm != nil {
		plan := Plan{
			URL:          probe.FinalURL,
			Output:       out,
			Size:         probe.Size,
			RangeOK:      probe.RangeOK,
			ETag:         probe.ETag,
			LastModified: probe.LastModified,
			ContentType:  probe.ContentType,
			Conns:        opts.Conns,
			Resume:       resuming,
			SHA256:       opts.SHA256,
		}
		if plan.Size > 0 && plan.RangeOK {
			plan.ChunkSize = chunkSizeFor(plan.Size, opts.Conns)
			plan.Chunks = int((plan.Size + plan.ChunkSize - 1) / plan.ChunkSize)
		}
		if resuming {
			plan.DoneBytes = st.ProgressBytes()
		}
		ok, err := opts.Confirm(plan)
		if err != nil {
			return err
		}
		if !ok {
			return ErrCancelled
		}
	}
	if opts.OnPlan != nil {
		plan := Plan{
			URL:          probe.FinalURL,
			Output:       out,
			Size:         probe.Size,
			RangeOK:      probe.RangeOK,
			ETag:         probe.ETag,
			LastModified: probe.LastModified,
			ContentType:  probe.ContentType,
			Conns:        opts.Conns,
			Resume:       resuming,
			SHA256:       opts.SHA256,
		}
		if plan.Size > 0 && plan.RangeOK {
			plan.ChunkSize = chunkSizeFor(plan.Size, opts.Conns)
			plan.Chunks = int((plan.Size + plan.ChunkSize - 1) / plan.ChunkSize)
		}
		if resuming {
			plan.DoneBytes = st.ProgressBytes()
		}
		opts.OnPlan(plan)
	}

	if resuming {
		st.URL = probe.FinalURL
		st.FinalURL = probe.FinalURL
		st.ETag = probe.ETag
		st.LastModified = probe.LastModified
		st.ContentType = probe.ContentType
		if opts.SHA256 != "" {
			st.SHA256 = strings.ToLower(opts.SHA256)
		}
		if err := st.Save(statePath); err != nil {
			return err
		}
	} else {
		if opts.Force {
			os.Remove(out)
		}
		st = NewState(probe, out, part, opts.Conns)
		st.SHA256 = strings.ToLower(opts.SHA256)
		if probe.RangeOK && probe.Size > 0 {
			head, tail, herr := edgeHashes(ctx, client, probe.FinalURL, opts.Headers, probe.Size)
			if herr == nil {
				st.HeadSHA = head
				st.TailSHA = tail
			}
		}
		if err := st.Save(statePath); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	saver := &stateSaver{path: statePath, st: st}
	label := filepath.Base(out)

	started := time.Now()
	var runErr error
	if probe.RangeOK && st.Size > 0 && len(st.Done) > 0 {
		runErr = downloadChunked(ctx, client, f, st, saver, label, opts)
	} else {
		runErr = downloadStream(ctx, client, f, st, saver, label, probe, opts)
	}
	if runErr != nil {
		_ = f.Sync()
		_ = saver.Save()
		return &ResumableError{Err: runErr, Output: out, URL: probe.FinalURL, SHA256: opts.SHA256}
	}

	if err := f.Sync(); err != nil {
		return &ResumableError{Err: err, Output: out, URL: probe.FinalURL, SHA256: opts.SHA256}
	}
	if err := f.Close(); err != nil {
		return &ResumableError{Err: err, Output: out, URL: probe.FinalURL, SHA256: opts.SHA256}
	}

	if st.Size > 0 {
		info, err := os.Stat(part)
		if err != nil {
			return &ResumableError{Err: err, Output: out, URL: probe.FinalURL, SHA256: opts.SHA256}
		}
		if info.Size() != st.Size {
			return &ResumableError{
				Err:    fmt.Errorf("size mismatch after download: got %d, want %d", info.Size(), st.Size),
				Output: out, URL: probe.FinalURL, SHA256: opts.SHA256,
			}
		}
	}

	expectedSHA := opts.SHA256
	if expectedSHA == "" {
		expectedSHA = st.SHA256
	}
	if expectedSHA != "" && !opts.Force {
		actual, err := fileSHA256(part)
		if err != nil {
			return &ResumableError{Err: err, Output: out, URL: probe.FinalURL, SHA256: expectedSHA}
		}
		if !strings.EqualFold(actual, expectedSHA) {
			st.VerifyFailed = true
			_ = saver.Save()
			// The transfer itself finished; only verification failed. Resuming
			// would just rename this bad file into place, so this is not a
			// resumable error.
			return fmt.Errorf("sha256 mismatch: got %s, want %s — the data is kept at %s; use --restart to download it again or --force to accept it",
				actual, strings.ToLower(expectedSHA), part)
		}
	}

	if err := os.Rename(part, out); err != nil {
		return &ResumableError{Err: err, Output: out, URL: probe.FinalURL, SHA256: opts.SHA256}
	}
	os.Remove(statePath)

	info, _ := os.Stat(out)
	bytes := st.Size
	if info != nil {
		bytes = info.Size()
	}
	sizeText := HumanBytes(bytes)
	elapsed := time.Since(started)
	if !opts.Quiet {
		speedText := ""
		if elapsed > 0 {
			speedText = ", " + HumanSpeed(float64(bytes)/elapsed.Seconds())
		}
		fmt.Printf("done: %s (%s in %s%s)\n", out, sizeText, shortDuration(elapsed), speedText)
	}
	if opts.Notify {
		notifySend("Download finished", filepath.Base(out)+" · "+sizeText)
	}
	return nil
}

// uniquePath returns path, or "name (1).ext", "name (2).ext", ... if taken.
func uniquePath(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !fileExists(cand) && !fileExists(cand+".part.zdl.json") {
			return cand
		}
	}
	return path
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

type stateSaver struct {
	mu   sync.Mutex
	path string
	st   *State
	last time.Time
}

func (s *stateSaver) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.st.Save(s.path)
	s.last = time.Now()
	return err
}

// SaveIfDue persists the state at most once per gap. Chunk completions can
// arrive hundreds of times per second on fast links; rewriting the whole state
// file for each one would burn CPU and disk for no extra safety, because the
// in-memory State is always current and Run saves it again on pause/exit.
func (s *stateSaver) SaveIfDue(gap time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.last) < gap {
		return nil
	}
	err := s.st.Save(s.path)
	s.last = time.Now()
	return err
}

// UpdateChunk records a worker's progress in memory and, unless the write is
// throttled, pushes it to disk. force is used when the chunk ended in an error
// (including a pause), so the offsets are never lost.
func (s *stateSaver) UpdateChunk(idx, progress int64, completed, force bool, gap time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int(idx) >= len(s.st.ChunkBytes) || int(idx) >= len(s.st.Done) {
		// Arrays are aligned before workers start; ignore stale indexes rather
		// than swapping the slice header while workers read it.
		return nil
	}
	s.st.ChunkBytes[idx] = min(progress, s.st.chunkLen(idx))
	if completed {
		s.st.Done[idx] = true
	}
	if !force && time.Since(s.last) < gap {
		return nil
	}
	err := s.st.Save(s.path)
	s.last = time.Now()
	return err
}

// ---------- resume validation ----------

func validateResume(ctx context.Context, client *http.Client, st *State, pr *ProbeResult, h Headers, force bool) error {
	if st.RangeOK && !pr.RangeOK {
		return fmt.Errorf("the new URL no longer supports byte ranges; cannot resume (use --restart)")
	}
	if st.Size != pr.Size {
		return fmt.Errorf("remote size changed: had %d bytes, now %d (use --restart to download again)",
			st.Size, pr.Size)
	}

	strongMatch := false
	if st.HeadSHA != "" && st.TailSHA != "" {
		head, tail, err := edgeHashes(ctx, client, pr.FinalURL, h, pr.Size)
		if err == nil {
			strongMatch = head == st.HeadSHA && tail == st.TailSHA
			if !strongMatch && !force {
				return fmt.Errorf("remote content changed (head/tail checksum mismatch); use --restart or --force")
			}
		}
	}

	if !strongMatch && !force {
		if st.ETag != "" && pr.ETag != "" && st.ETag != pr.ETag {
			// Some servers mint a new ETag for the same object on every request.
			// The strong hashes above are the tie-breaker; without them, refuse.
			return fmt.Errorf("ETag changed (%s -> %s); use --force if the file is really the same", st.ETag, pr.ETag)
		}
		if st.LastModified != "" && pr.LastModified != "" && st.LastModified != pr.LastModified {
			return fmt.Errorf("Last-Modified changed (%s -> %s); use --force if the file is really the same",
				st.LastModified, pr.LastModified)
		}
	}
	return nil
}

// ---------- chunked download ----------

func downloadChunked(ctx context.Context, client *http.Client, f *os.File, st *State,
	saver *stateSaver, label string, opts Options) error {

	if err := preallocate(f, st.Size); err != nil {
		return err
	}

	var done atomic.Int64
	done.Store(st.ProgressBytes())

	// Older state files predate ChunkBytes; align the arrays.
	if len(st.ChunkBytes) != len(st.Done) {
		grown := make([]int64, len(st.Done))
		copy(grown, st.ChunkBytes)
		st.ChunkBytes = grown
	}

	var pending []int64
	for i, d := range st.Done {
		if !d {
			pending = append(pending, int64(i))
		}
	}
	if len(pending) == 0 {
		return nil
	}

	pctx, pcancel := context.WithCancel(ctx)
	prog := NewProgress(label, st.Size, &done, min(opts.Conns, len(pending)), opts.Quiet, opts.Progress)
	go prog.Run(pctx)
	defer func() {
		pcancel()
		<-prog.Done()
	}()

	var limiter *RateLimiter
	if opts.Limiter != nil {
		limiter = opts.Limiter
	} else if opts.Rate > 0 {
		limiter = NewRateLimiter(opts.Rate)
	}

	jobs := make(chan int64, len(pending))
	for _, i := range pending {
		jobs <- i
	}
	close(jobs)

	dctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	workers := opts.Conns
	if workers > len(pending) {
		workers = len(pending)
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if dctx.Err() != nil {
					return
				}
				progress, err := fetchChunkWithRetry(dctx, client, st, f, idx, opts, opts.Headers, &done, limiter)
				// Update the shared state in memory every time, but only
				// rewrite the (potentially large) JSON once a second unless
				// the chunk failed or was paused.
				saveErr := saver.UpdateChunk(idx, progress, err == nil, err != nil, time.Second)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				if saveErr != nil {
					select {
					case errCh <- fmt.Errorf("saving state: %w", saveErr):
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if st.CompletedBytes() < st.Size {
		return fmt.Errorf("download incomplete: %d/%d bytes", st.CompletedBytes(), st.Size)
	}
	prog.MarkSuccess()
	return saver.Save()
}

func fetchChunkWithRetry(ctx context.Context, client *http.Client, st *State, f *os.File,
	idx int64, opts Options, h Headers, done *atomic.Int64, limiter *RateLimiter) (int64, error) {

	chunkStart := idx * st.ChunkSize
	chunkLen := st.chunkLen(idx)
	if chunkLen <= 0 {
		return 0, nil
	}
	progress := min(st.ChunkProgress(idx), chunkLen)
	if progress >= chunkLen {
		return chunkLen, nil
	}
	end := chunkStart + chunkLen - 1

	var lastErr error
	for attempt := 0; attempt <= opts.Retries; attempt++ {
		if attempt > 0 {
			d := retryDelay(attempt, lastErr)
			if err := sleepCtx(ctx, d); err != nil {
				return progress, err
			}
		}

		start := chunkStart + progress
		var written int64
		err := fetchRange(ctx, client, st, f, h, start, end, limiter, func(n int) {
			written += int64(n)
			done.Add(int64(n))
		})
		// Keep whatever landed on disk: the next attempt continues from here
		// instead of re-fetching the whole chunk.
		progress += written
		if err == nil {
			if progress < chunkLen {
				return progress, fmt.Errorf("chunk %d short read: %d/%d bytes", idx, progress, chunkLen)
			}
			return chunkLen, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return progress, ctx.Err()
		}

		var he *HTTPError
		if errors.As(err, &he) {
			switch he.Status {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusGone:
				return progress, fmt.Errorf("%w — the link may have expired; retry with a new --url, or --cookies-from-zen", err)
			case http.StatusOK:
				return progress, fmt.Errorf("%w — the server ignored Range (the remote file changed or lost range support); use --restart", err)
			case http.StatusRequestedRangeNotSatisfiable:
				return progress, fmt.Errorf("%w — the remote file no longer matches the saved state; use --restart", err)
			}
		}
	}
	return progress, fmt.Errorf("chunk %d failed after %d attempts: %w", idx, opts.Retries+1, lastErr)
}

func fetchRange(ctx context.Context, client *http.Client, st *State, f *os.File, h Headers,
	start, end int64, limiter *RateLimiter, onWrite func(int)) error {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.URL, nil)
	if err != nil {
		return err
	}
	h.apply(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	applyStateHeaders(req, st)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		body := readSnippet(resp.Body)
		if resp.StatusCode == http.StatusOK {
			// 200 here means the "body" is the whole file, not an error page.
			body = ""
		}
		return &HTTPError{
			Status:     resp.StatusCode,
			URL:        st.URL,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Body:       body,
		}
	}

	if crStart, _, _, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && crStart != start {
		return fmt.Errorf("server returned range starting at %d, expected %d", crStart, start)
	}

	want := end - start + 1
	w := &offsetWriter{f: f, off: start, limiter: limiter, onWrite: onWrite}
	buf := acquireCopyBuf()
	n, err := io.CopyBuffer(w, io.LimitReader(resp.Body, want), buf)
	releaseCopyBuf(buf)
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("short ranged response: got %d of %d bytes", n, want)
	}
	return nil
}

// applyStateHeaders adds the validators to ranged requests. If the remote
// object changed, If-Range makes the server answer 200 instead of 206, which
// we treat as fatal instead of silently mixing two versions.
func applyStateHeaders(req *http.Request, st *State) {
	if st == nil {
		return
	}
	if st.ETag != "" && !strings.HasPrefix(st.ETag, "W/") {
		req.Header.Set("If-Range", st.ETag)
	} else if st.LastModified != "" {
		req.Header.Set("If-Range", st.LastModified)
	}
}

// ---------- stream (no range support) ----------

func downloadStream(ctx context.Context, client *http.Client, f *os.File, st *State,
	saver *stateSaver, label string, pr *ProbeResult, opts Options) error {

	var done atomic.Int64
	base := st.DoneBytes
	if base < 0 {
		base = 0
	}
	done.Store(base)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.URL, nil)
	if err != nil {
		return err
	}
	opts.Headers.apply(req)
	if base > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", base))
		applyStateHeaders(req, st)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Range ignored: start from zero rather than corrupting the file.
		base = 0
		done.Store(0)
		st.DoneBytes = 0
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	case http.StatusPartialContent:
		if crStart, _, _, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && crStart != base {
			return fmt.Errorf("server returned range starting at %d, expected %d", crStart, base)
		}
		if _, err := f.Seek(base, io.SeekStart); err != nil {
			return err
		}
	default:
		return &HTTPError{Status: resp.StatusCode, URL: st.URL, Body: readSnippet(resp.Body)}
	}

	pctx, pcancel := context.WithCancel(ctx)
	prog := NewProgress(label, st.Size, &done, 1, opts.Quiet, opts.Progress)
	go prog.Run(pctx)
	defer func() {
		pcancel()
		<-prog.Done()
	}()

	var limiter *RateLimiter
	if opts.Limiter != nil {
		limiter = opts.Limiter
	} else if opts.Rate > 0 {
		limiter = NewRateLimiter(opts.Rate)
	}

	w := &streamWriter{
		f:        f,
		off:      base,
		st:       st,
		saver:    saver,
		progress: &done,
		limiter:  limiter,
		lastSave: time.Now(),
	}
	buf := acquireCopyBuf()
	_, err = io.CopyBuffer(w, resp.Body, buf)
	releaseCopyBuf(buf)
	if err != nil {
		_ = saver.Save()
		return err
	}
	st.DoneBytes = w.off
	if pr.Size > 0 && w.off != pr.Size {
		_ = saver.Save()
		return fmt.Errorf("size mismatch: got %d of %d bytes", w.off, pr.Size)
	}
	if pr.Size > 0 {
		st.Size = pr.Size
	}
	prog.MarkSuccess()
	return saver.Save()
}

type streamWriter struct {
	f        *os.File
	off      int64
	st       *State
	saver    *stateSaver
	progress *atomic.Int64
	limiter  *RateLimiter
	lastSave time.Time
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.limiter != nil {
		w.limiter.Wait(len(p))
	}
	n, err := w.f.WriteAt(p, w.off)
	if n > 0 {
		w.off += int64(n)
		w.progress.Add(int64(n))
	}
	if time.Since(w.lastSave) > time.Second {
		w.st.DoneBytes = w.off
		_ = w.saver.Save()
		w.lastSave = time.Now()
	}
	return n, err
}

// ---------- edge hashes ----------

func edgeHashes(ctx context.Context, client *http.Client, rawURL string, h Headers, size int64) (string, string, error) {
	const window = 64 << 10
	if size <= 0 {
		return "", "", fmt.Errorf("unknown size")
	}
	if size <= 2*window {
		data, err := fetchBytes(ctx, client, rawURL, h, 0, size-1)
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256(data)
		s := hex.EncodeToString(sum[:])
		return s, s, nil
	}
	// Fetch both edge windows concurrently: on a high-latency link this halves
	// the pre-transfer validation cost before a resume can start.
	type edgeResult struct {
		sum string
		err error
	}
	headCh := make(chan edgeResult, 1)
	tailCh := make(chan edgeResult, 1)
	go func() {
		s, err := hashRange(ctx, client, rawURL, h, 0, window-1)
		headCh <- edgeResult{s, err}
	}()
	go func() {
		s, err := hashRange(ctx, client, rawURL, h, size-window, size-1)
		tailCh <- edgeResult{s, err}
	}()
	head, tail := <-headCh, <-tailCh
	if head.err != nil {
		return "", "", head.err
	}
	if tail.err != nil {
		return "", "", tail.err
	}
	return head.sum, tail.sum, nil
}

func fetchBytes(ctx context.Context, client *http.Client, rawURL string, h Headers, start, end int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	h.apply(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, &HTTPError{Status: resp.StatusCode, URL: rawURL, Body: readSnippet(resp.Body)}
	}
	return io.ReadAll(io.LimitReader(resp.Body, end-start+1))
}

func hashRange(ctx context.Context, client *http.Client, rawURL string, h Headers, start, end int64) (string, error) {
	data, err := fetchBytes(ctx, client, rawURL, h, start, end)
	if err != nil {
		return "", err
	}
	if int64(len(data)) != end-start+1 {
		return "", fmt.Errorf("short hash range: got %d bytes", len(data))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ---------- helpers ----------

func retryDelay(attempt int, err error) time.Duration {
	var he *HTTPError
	if errors.As(err, &he) && he.RetryAfter > 0 {
		if he.RetryAfter > 60*time.Second {
			return 60 * time.Second
		}
		return he.RetryAfter
	}
	d := time.Duration(1<<uint(min(attempt, 5))) * time.Second
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(d/2) + 1))
	return d + jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func notifySend(title, body string) {
	cmd := exec.Command("notify-send", "-a", "zdl", title, body)
	_ = cmd.Run()
}
