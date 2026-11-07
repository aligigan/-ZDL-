package downloader

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

// NewClient builds one shared client for the probe and every worker.
// HTTP/2 is deliberately left off: over h2 all ranged requests would be
// multiplexed onto a single TCP connection, which defeats the point of -x.
func NewClient(conns int, insecure bool) *http.Client {
	if conns < 1 {
		conns = 1
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          conns * 2,
		MaxIdleConnsPerHost:   conns,
		MaxConnsPerHost:       conns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure},
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}
