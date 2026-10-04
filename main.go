package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	q := strings.TrimSpace(qs.Get("q"))
	eng, ok := engines[qs.Get("engine")]
	switch {
	case !ok:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose Yandex, Pinterest or Danbooru."})
		return
	case q == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Type something to search for."})
		return
	case len(q) > 200:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Search is too long (200 characters max)."})
		return
	}
	opt := Options{Safe: qs.Get("safe") != "0"}
	cursor := qs.Get("cursor")
	key := fmt.Sprintf("%s|%s|%s|%t", qs.Get("engine"), q, cursor, opt.Safe)
	if p, hit := cacheGet(key); hit {
		writeJSON(w, http.StatusOK, p)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	page, err := eng.Search(ctx, q, cursor, opt)
	if err != nil {
		status := http.StatusBadGateway
		msg := err.Error()
		switch {
		case errors.Is(err, errCaptcha):
			status = http.StatusTooManyRequests
		case errors.Is(err, context.DeadlineExceeded):
			msg = "The search engine took too long to answer."
		}
		log.Printf("search %s %q: %v", qs.Get("engine"), q, err)
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if page.Results == nil {
		page.Results = []Image{}
	}
	cacheSet(key, page)
	writeJSON(w, http.StatusOK, page)
}

func main() {
	addr := flag.String("addr", envOr("ADDR", "127.0.0.1:8080"), "listen address (host:port)")
	flag.Parse()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/search", handleSearch)
	mux.HandleFunc("/api/img", handleImage)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      90 * time.Second,
	}
	log.Printf("Prism is running at http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}
