package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

// The image proxy lets the UI download images (the "download" attribute is
// ignored cross-origin) and recover from hotlink protection. It refuses to
// connect to loopback/private/link-local addresses so it can't be abused to
// reach internal services.
func blockPrivate(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return errors.New("blocked address")
	}
	return nil
}

var safeClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           dialWithFallback(blockPrivate),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
	},
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return -1
	}, s)
	if s == "" || s == "." {
		return "image"
	}
	return s
}

func handleImage(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(r.URL.Query().Get("u"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.5")
	if strings.HasSuffix(u.Hostname(), "donmai.us") {
		req.Header.Set("User-Agent", botUA)
	}

	resp, err := safeClient.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	if r.URL.Query().Get("dl") == "1" {
		h.Set("Content-Disposition", `attachment; filename="`+safeName(path.Base(u.Path))+`"`)
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 40<<20))
}
