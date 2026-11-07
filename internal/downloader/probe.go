package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is everything we learned before starting to transfer bytes.
type ProbeResult struct {
	FinalURL     string
	Size         int64 // -1 when unknown
	RangeOK      bool
	ETag         string
	LastModified string
	ContentType  string
	Disposition  string
	Filename     string
}

// HTTPError carries a status code so retry logic can decide what to do.
type HTTPError struct {
	Status     int
	URL        string
	RetryAfter time.Duration
	Body       string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("HTTP %d", e.Status)
	if e.URL != "" {
		msg += " for " + e.URL
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

func probeMeta(pr *ProbeResult, resp *http.Response) {
	pr.FinalURL = resp.Request.URL.String()
	if v := resp.Header.Get("ETag"); v != "" {
		pr.ETag = v
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		pr.LastModified = v
	}
	if v := resp.Header.Get("Content-Type"); v != "" {
		pr.ContentType = v
	}
	if v := resp.Header.Get("Content-Disposition"); v != "" {
		pr.Disposition = v
	}
}

// Probe first asks with HEAD, then verifies byte-range support with a tiny GET.
// Returns an error only when the URL is not usable at all.
func Probe(ctx context.Context, client *http.Client, rawURL string, h Headers) (*ProbeResult, error) {
	pr := &ProbeResult{FinalURL: rawURL, Size: -1}

	var headErr error
	headOK := false
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil); err == nil {
		h.apply(req)
		if resp, err := client.Do(req); err == nil {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					headOK = true
					probeMeta(pr, resp)
					pr.Size = resp.ContentLength
					pr.RangeOK = strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes")
				} else {
					headErr = &HTTPError{Status: resp.StatusCode, URL: rawURL, Body: readSnippet(resp.Body)}
				}
			}()
		} else {
			headErr = err
		}
	}

	// HEAD is often blocked or lies; a Range: bytes=0-0 GET is authoritative.
	rangeErr := confirmRange(ctx, client, rawURL, h, pr, headOK)
	if rangeErr != nil {
		if !headOK {
			if headErr != nil {
				return nil, headErr
			}
			return nil, rangeErr
		}
		pr.RangeOK = false
	}

	if pr.Size < 0 && headOK && !pr.RangeOK {
		// Still unknown; a plain GET would tell us, but stream mode handles it.
		pr.Size = -1
	}

	pr.Filename = sanitizeFilename(filenameFromDisposition(pr.Disposition))
	if pr.Filename == "" {
		pr.Filename = sanitizeFilename(filenameFromURL(pr.FinalURL))
	}
	if pr.Filename == "" {
		pr.Filename = "download.bin"
	}
	return pr, nil
}

func confirmRange(ctx context.Context, client *http.Client, rawURL string, h Headers, pr *ProbeResult, headOK bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	h.apply(req)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		pr.RangeOK = true
		probeMeta(pr, resp)
		if start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
			_ = start
			if total >= 0 {
				pr.Size = total
			}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	case http.StatusOK:
		pr.RangeOK = false
		probeMeta(pr, resp)
		if pr.Size < 0 {
			pr.Size = resp.ContentLength
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	default:
		return &HTTPError{Status: resp.StatusCode, URL: rawURL, Body: readSnippet(resp.Body)}
	}
	return nil
}

// parseContentRange understands "bytes 0-0/12345" and "bytes 0-0/*".
func parseContentRange(v string) (start, end, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(strings.ToLower(v), "bytes ") {
		return 0, 0, -1, false
	}
	v = strings.TrimSpace(v[len("bytes "):])
	slash := strings.IndexByte(v, '/')
	if slash < 0 {
		return 0, 0, -1, false
	}
	span, sizeStr := v[:slash], v[slash+1:]
	dash := strings.IndexByte(span, '-')
	if dash < 0 {
		return 0, 0, -1, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(span[:dash]), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(span[dash+1:]), 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, -1, false
	}
	total = -1
	if sizeStr != "*" {
		if n, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64); err == nil {
			total = n
		}
	}
	return start, end, total, true
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 300))
	// Error bodies are sometimes binary or HTML noise; keep the message short
	// and printable so it stays readable in a terminal.
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch {
		case c >= 32 && c < 127:
			out = append(out, c)
		case c == '\n' || c == '\r' || c == '\t':
			out = append(out, ' ')
		}
	}
	s := strings.Join(strings.Fields(string(out)), " ")
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func filenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if name == "/" || name == "." {
		return ""
	}
	return name
}

// filenameFromDisposition handles both filename= and RFC 5987 filename*=.
func filenameFromDisposition(cd string) string {
	if strings.TrimSpace(cd) == "" {
		return ""
	}
	lower := strings.ToLower(cd)
	if i := strings.Index(lower, "filename*="); i >= 0 {
		v := cutParam(cd[i+len("filename*="):])
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		parts := strings.SplitN(v, "'", 3)
		if len(parts) == 3 {
			if dec, err := url.PathUnescape(parts[2]); err == nil {
				return dec
			}
			return parts[2]
		}
		return v
	}
	if i := strings.Index(lower, "filename="); i >= 0 {
		v := cutParam(cd[i+len("filename="):])
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"`)
		return v
	}
	return ""
}

func cutParam(v string) string {
	inQuote := false
	for i, r := range v {
		switch r {
		case '"':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				return v[:i]
			}
		}
	}
	return v
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if len(name) > 200 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name
}
