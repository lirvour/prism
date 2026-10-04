package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Danbooru has a public JSON API; no scraping needed. Anonymous users are
// limited to 2 tags per search.
type danbooru struct{}

type dPost struct {
	ID           int    `json:"id"`
	Rating       string `json:"rating"`
	Width        int    `json:"image_width"`
	Height       int    `json:"image_height"`
	FileExt      string `json:"file_ext"`
	FileURL      string `json:"file_url"`
	LargeFileURL string `json:"large_file_url"`
	PreviewURL   string `json:"preview_file_url"`
	Artist       string `json:"tag_string_artist"`
	Character    string `json:"tag_string_character"`
	Copyright    string `json:"tag_string_copyright"`
}

func firstTag(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.ReplaceAll(f[0], "_", " ")
}

func (danbooru) Search(ctx context.Context, q, cursor string, o Options) (*Page, error) {
	page := atoiDefault(cursor, 1)
	if page < 1 {
		page = 1
	}
	query := url.Values{
		"tags":  {q},
		"limit": {"40"},
		"page":  {strconv.Itoa(page)},
	}.Encode()

	// If the main host is unreachable, try mirrors. safebooru only serves
	// safe posts, so it is used as a fallback in safe mode only.
	hosts := []string{"danbooru.donmai.us"}
	if h := strings.TrimSpace(os.Getenv("DANBOORU_HOST")); h != "" {
		hosts = []string{h}
	}
	if o.Safe {
		hosts = append(hosts, "safebooru.donmai.us")
	}
	var res *fetched
	var err error
	host := ""
	for _, h := range hosts {
		host = h
		res, err = fetch(ctx, "https://"+h+"/posts.json?"+query, map[string]string{
			"Accept":     "application/json",
			"User-Agent": botUA,
		})
		if err == nil && res.Status != 403 && res.Status < 500 {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("could not reach Danbooru (%v). Check your connection or set DANBOORU_HOST to a reachable mirror", err)
	}
	body := strings.TrimSpace(string(res.Body))
	if strings.HasPrefix(body, "{") {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(res.Body, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("HTTP %d", res.Status)
		}
		return nil, fmt.Errorf("Danbooru: %s", e.Message)
	}
	if res.Status != 200 {
		snip := strings.Join(strings.Fields(reTags.ReplaceAllString(body, " ")), " ")
		if len(snip) > 140 {
			snip = snip[:140]
		}
		return nil, fmt.Errorf("Danbooru answered with HTTP %d: %s", res.Status, snip)
	}

	var posts []dPost
	if err := json.Unmarshal(res.Body, &posts); err != nil {
		return nil, fmt.Errorf("Danbooru returned an unexpected response")
	}

	out := []Image{}
	for _, p := range posts {
		switch p.FileExt {
		case "mp4", "webm", "zip", "swf":
			continue
		}
		if o.Safe && p.Rating != "g" && p.Rating != "s" {
			continue
		}
		full := p.FileURL
		if full == "" {
			full = p.LargeFileURL
		}
		thumb := p.LargeFileURL
		if thumb == "" {
			thumb = p.PreviewURL
		}
		if full == "" || thumb == "" {
			continue
		}
		title := firstTag(p.Character)
		if c := firstTag(p.Copyright); c != "" {
			if title != "" {
				title += " (" + c + ")"
			} else {
				title = c
			}
		}
		out = append(out, Image{
			ID:     strconv.Itoa(p.ID),
			Thumb:  thumb,
			Full:   full,
			Title:  title,
			Meta:   firstTag(p.Artist),
			Source: "https://" + host + "/posts/" + strconv.Itoa(p.ID),
			Width:  p.Width,
			Height: p.Height,
		})
	}

	next := ""
	if len(posts) > 0 {
		next = strconv.Itoa(page + 1)
	}
	return &Page{Results: out, Next: next}, nil
}
