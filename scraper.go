package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Danbooru's Cloudflare rules reject browser-spoofing user agents on the API
// (403); it wants an honest, descriptive bot UA instead.
const botUA = "Prism/1.0 (local image metasearch)"

var errCaptcha = errors.New("the engine asked for a captcha; wait a few minutes or switch engines")

// Image is the engine-agnostic result sent to the front-end.
type Image struct {
	ID     string `json:"id"`
	Thumb  string `json:"thumb"`
	Full   string `json:"full"`
	Title  string `json:"title,omitempty"`
	Meta   string `json:"meta,omitempty"`
	Source string `json:"source,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Page is one batch of results plus the cursor for the next batch.
type Page struct {
	Results []Image `json:"results"`
	Next    string  `json:"next,omitempty"`
}

type Options struct{ Safe bool }

type Engine interface {
	Search(ctx context.Context, q, cursor string, o Options) (*Page, error)
}

var engines = map[string]Engine{
	"yandex":    yandex{},
	"pinterest": pinterest{},
	"danbooru":  danbooru{},
}

// fallbackDNS is used only when the system resolver cannot find a host (some
// ISPs/networks block domains such as danbooru.donmai.us at DNS level).
// Override with DNS_SERVERS="9.9.9.9:53,1.1.1.1:53".
func fallbackDNS() []string {
	if v := strings.TrimSpace(os.Getenv("DNS_SERVERS")); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}
}

type dohAnswer struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

var dohEndpoints = []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/resolve", "https://1.0.0.1/dns-query"}

var dohClient = &http.Client{Timeout: 6 * time.Second}

// lookupDoH resolves a host over HTTPS using IP-literal endpoints, which works
// on networks that intercept or block plain DNS (UDP/TCP port 53).
func lookupDoH(ctx context.Context, host string) []string {
	for _, ep := range dohEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep+"?"+url.Values{"name": {host}, "type": {"A"}}.Encode(), nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/dns-json")
		resp, err := dohClient.Do(req)
		if err != nil {
			continue
		}
		var ans dohAnswer
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ans)
		resp.Body.Close()
		if err != nil {
			continue
		}
		var ips []string
		for _, r := range ans.Answer {
			if r.Type == 1 {
				ips = append(ips, r.Data)
			}
		}
		if len(ips) > 0 {
			return ips
		}
	}
	return nil
}

func dialWithFallback(control func(network, address string, c syscall.RawConn) error) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: control}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		var dnsErr *net.DNSError
		if err == nil || !errors.As(err, &dnsErr) {
			return conn, err
		}
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return nil, err
		}
		tryIPs := func(ips []string) net.Conn {
			for _, ip := range ips {
				if c, cerr := d.DialContext(ctx, network, net.JoinHostPort(ip, port)); cerr == nil {
					return c
				}
			}
			return nil
		}
		// 1) other DNS servers over UDP
		for _, srv := range fallbackDNS() {
			srv := strings.TrimSpace(srv)
			res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "udp", srv)
			}}
			ips, lerr := res.LookupHost(ctx, host)
			if lerr != nil {
				continue
			}
			if c := tryIPs(ips); c != nil {
				return c, nil
			}
		}
		// 2) DNS over HTTPS, for networks that intercept port 53
		if c := tryIPs(lookupDoH(ctx, host)); c != nil {
			return c, nil
		}
		return nil, err
	}
}

var client = func() *http.Client {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialWithFallback(nil)
	return &http.Client{Timeout: 20 * time.Second, Jar: jar, Transport: tr}
}()

type fetched struct {
	Body   []byte
	Status int
	URL    string
}

func fetch(ctx context.Context, rawURL string, hdr map[string]string) (*fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	return &fetched{Body: body, Status: resp.StatusCode, URL: resp.Request.URL.String()}, nil
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return d
}

// ---- tiny in-memory TTL cache so scrolling back / re-searching is instant ----

type cacheEntry struct {
	page *Page
	exp  time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cacheEntry{}
)

func cacheGet(k string) (*Page, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := cache[k]
	if !ok || time.Now().After(e.exp) {
		delete(cache, k)
		return nil, false
	}
	return e.page, true
}

func cacheSet(k string, p *Page) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if len(cache) > 500 {
		for key := range cache {
			delete(cache, key)
		}
	}
	cache[k] = cacheEntry{page: p, exp: time.Now().Add(5 * time.Minute)}
}
