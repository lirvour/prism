# Prism: image metasearch (Go + HTML)

Search Yandex, Pinterest or Danbooru from one clean UI. Standard library only: no dependencies to download.

## Run
    go run .                    # http://127.0.0.1:8080
    go run . -addr :3000        # custom address (or ADDR=:3000)

## Build
    go build -o prism .         # single binary, UI is embedded
    ./build.sh                  # cross-compiles linux/mac/windows into ./dist

Needs Go 1.21 or newer.

## How each engine works
- Yandex: reads the server-rendered results page and extracts the `serp-item` JSON blobs (same idea as 4get). Paged with `p=`. Yandex may show a captcha after many requests; the UI says so.
- Pinterest: the internal `BaseSearchResource` endpoint (as in Binternet), paged with Pinterest's bookmark token.
- Danbooru: public JSON API (`/posts.json`). Anonymous accounts can use 2 tags per search. "Safe results only" keeps ratings g and s.

`/api/img?u=...` is a small image proxy for downloads and hotlink fallbacks. It only returns `image/*` responses and refuses private/loopback addresses.

## Files
main.go (server, /api/search), scraper.go (types, HTTP, cache), yandex.go, pinterest.go, danbooru.go, proxy.go, web/index.html (UI)

## Troubleshooting
- Danbooru "no such host": your DNS blocks the domain. Prism retries with 1.1.1.1 / 8.8.8.8 / 9.9.9.9 (override with `DNS_SERVERS`), then DNS-over-HTTPS. If your ISP also blocks the site by SNI/IP, use a VPN or a mirror (`DANBOORU_HOST=...`).
- Danbooru 403: Cloudflare rejects browser-like user agents on the API; Prism sends an honest bot UA (`botUA` in scraper.go).
