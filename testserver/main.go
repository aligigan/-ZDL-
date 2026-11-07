package main

import (
	"bytes"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type server struct {
	mu        sync.Mutex
	content   []byte
	etag      string
	token     string
	fixedTime time.Time
	failRate  int
	slowDelay time.Duration
}

func newServer(size int64) *server {
	return &server{
		content:   makeContent(size),
		etag:      `"v1"`,
		token:     "abc",
		fixedTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// makeContent is deterministic so the client can verify it byte-for-byte.
func makeContent(n int64) []byte { return makeContentSeed(n, 0) }

// makeContentSeed produces a different same-length body for content-change tests.
func makeContentSeed(n int64, seed int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*31 + 7 + seed*13) % 251)
	}
	return b
}

func (s *server) snapshot() ([]byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content, s.etag
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request, allowRange bool) {
	content, etag := s.snapshot()
	w.Header().Set("Etag", etag)
	w.Header().Set("Content-Disposition", `attachment; filename="testfile.bin"`)
	if !allowRange {
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
		return
	}
	http.ServeContent(w, r, "testfile.bin", s.fixedTime, bytes.NewReader(content))
}

func main() {
	addr := "127.0.0.1:8099"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	s := newServer(64 << 20)

	mux := http.NewServeMux()

	mux.HandleFunc("/file.bin", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s range=%q", r.Method, r.URL.String(), r.Header.Get("Range"))
		s.serveFile(w, r, true)
	})

	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, "<!doctype html><title>just a page</title><p>not a file</p>")
	})

	mux.HandleFunc("/norange", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s (no ranges)", r.Method, r.URL.String())
		s.serveFile(w, r, false)
	})

	mux.HandleFunc("/expire", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		token := s.token
		s.mu.Unlock()
		if r.URL.Query().Get("token") != token {
			http.Error(w, "expired link", http.StatusForbidden)
			return
		}
		log.Printf("%s %s token=%s", r.Method, r.URL.String(), token)
		s.serveFile(w, r, true)
	})

	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value != "ok" {
			http.Error(w, "cookie required", http.StatusUnauthorized)
			return
		}
		log.Printf("%s %s (authenticated)", r.Method, r.URL.String())
		s.serveFile(w, r, true)
	})

	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rate := s.failRate
		s.mu.Unlock()
		if rate > 0 && rand.Intn(100) < rate {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		s.serveFile(w, r, true)
	})

	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		if ms, _ := strconv.Atoi(r.URL.Query().Get("ms")); ms > 0 {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		delay := s.slowDelay
		s.mu.Unlock()
		s.serveFile(w, r, true)
		if delay > 0 {
			time.Sleep(delay)
		}
	})

	mux.HandleFunc("/switch", func(w http.ResponseWriter, r *http.Request) {
		size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
		if size <= 0 {
			size = 40 << 20
		}
		s.mu.Lock()
		s.content = makeContent(size)
		s.etag = fmt.Sprintf(`"v%d"`, size)
		s.mu.Unlock()
		fmt.Fprintf(w, "content now %d bytes, etag %q\n", size, s.etag)
	})

	// /liar advertises range support for the 1-byte probe but ignores it for
	// real chunks, which must be fatal rather than silently corrupting data.
	mux.HandleFunc("/liar", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			s.serveFile(w, r, true)
			return
		}
		s.serveFile(w, r, false)
	})

	mux.HandleFunc("/mutate", func(w http.ResponseWriter, r *http.Request) {
		seed, _ := strconv.Atoi(r.URL.Query().Get("seed"))
		if seed == 0 {
			seed = 1
		}
		s.mu.Lock()
		s.content = makeContentSeed(int64(len(s.content)), seed)
		s.etag = fmt.Sprintf(`"s%d"`, seed)
		s.mu.Unlock()
		fmt.Fprintf(w, "content mutated, same size, etag %q\n", s.etag)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("t")
		if t == "" {
			t = "next"
		}
		s.mu.Lock()
		s.token = t
		s.mu.Unlock()
		fmt.Fprintf(w, "token now %q\n", t)
	})

	mux.HandleFunc("/flakyrate", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("p"))
		s.mu.Lock()
		s.failRate = n
		s.mu.Unlock()
		fmt.Fprintf(w, "failure rate %d%%\n", n)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "zdl test server")
		fmt.Fprintln(w, "  /file.bin        64 MiB, ranges, etag, attachment")
		fmt.Fprintln(w, "  /norange         ignores Range")
		fmt.Fprintln(w, "  /expire?token=X  valid only for the current token")
		fmt.Fprintln(w, "  /auth            requires Cookie: session=ok")
		fmt.Fprintln(w, "  /flaky           random 503s (/flakyrate?p=30)")
		fmt.Fprintln(w, "  /switch?size=N   change content/etag")
		fmt.Fprintln(w, "  /mutate?seed=N   different bytes, same size")
		fmt.Fprintln(w, "  /token?t=X       rotate the /expire token")
	})

	log.Printf("listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
