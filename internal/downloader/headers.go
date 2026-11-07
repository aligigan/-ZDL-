package downloader

import "net/http"

// DefaultUserAgent keeps CDNs from treating us like a scraper.
const DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:159.0) Gecko/20100101 Firefox/159.0"

// Headers is everything the browser would normally send for a download.
type Headers struct {
	UserAgent string
	Referer   string
	Cookie    string
	Extra     map[string]string
}

func (h Headers) apply(req *http.Request) {
	ua := h.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	// Never let a server transparently compress a ranged body.
	req.Header.Set("Accept-Encoding", "identity")
	if h.Referer != "" {
		req.Header.Set("Referer", h.Referer)
	}
	if h.Cookie != "" {
		req.Header.Set("Cookie", h.Cookie)
	}
	for k, v := range h.Extra {
		req.Header.Set(k, v)
	}
}
