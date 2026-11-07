package downloader

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// copyBufSize is the read buffer used by transfer workers. Bigger than
// io.Copy's 32 KiB default, which measurably helps on high-bandwidth links.
const copyBufSize = 128 << 10

var copyBufPool = sync.Pool{
	New: func() any { return make([]byte, copyBufSize) },
}

func acquireCopyBuf() []byte {
	if b := copyBufPool.Get(); b != nil {
		return b.([]byte)
	}
	return make([]byte, copyBufSize)
}

func releaseCopyBuf(b []byte) {
	if cap(b) >= copyBufSize {
		copyBufPool.Put(b[:copyBufSize])
	}
}

// HumanBytes renders 12345678 as 11.8 MiB.
func HumanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	val := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
	if val >= 100 {
		return fmt.Sprintf("%.0f %s", val, suffix)
	}
	return fmt.Sprintf("%.1f %s", val, suffix)
}

func HumanSpeed(bps float64) string {
	if bps < 0 {
		return "?"
	}
	return HumanBytes(int64(bps)) + "/s"
}

// shortDuration keeps sub-minute transfers at one decimal place.
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return HumanETA(d)
}

func HumanETA(d time.Duration) string {
	if d <= 0 || d > 99*time.Hour {
		return "--"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// offsetWriter writes each chunk at its own offset in the shared file.
// It also feeds the global progress counter and the optional rate limiter.
type offsetWriter struct {
	f       *os.File
	off     int64
	limiter *RateLimiter
	onWrite func(int)
	written int64
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	if w.limiter != nil {
		w.limiter.Wait(len(p))
	}
	n, err := w.f.WriteAt(p, w.off)
	if n > 0 {
		w.off += int64(n)
		w.written += int64(n)
		if w.onWrite != nil {
			w.onWrite(n)
		}
	}
	return n, err
}

// RateLimiter is a simple shared token bucket measured in bytes/second.
type RateLimiter struct {
	mu   sync.Mutex
	rate float64
	next time.Time
}

func NewRateLimiter(bytesPerSecond int64) *RateLimiter {
	return &RateLimiter{rate: float64(bytesPerSecond)}
}

func (l *RateLimiter) Wait(n int) {
	if l == nil || l.rate <= 0 || n <= 0 {
		return
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	d := time.Until(l.next)
	l.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}
