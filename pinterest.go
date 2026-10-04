package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Pinterest: same internal resource endpoint Binternet uses. Pagination is a
// "bookmark" token returned with every page; "-end-" means no more results.
type pinterest struct{}

type pinImage struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type pinResponse struct {
	ResourceResponse struct {
		Data struct {
			Results []struct {
				ID        string              `json:"id"`
				GridTitle string              `json:"grid_title"`
				Title     string              `json:"title"`
				Link      string              `json:"link"`
				Images    map[string]pinImage `json:"images"`
			} `json:"results"`
		} `json:"data"`
		Bookmark string `json:"bookmark"`
	} `json:"resource_response"`
}

func (pinterest) Search(ctx context.Context, q, cursor string, _ Options) (*Page, error) {
	if cursor == "-end-" {
		return &Page{Results: []Image{}}, nil
	}
	opts := map[string]any{"query": q, "scope": "pins", "rs": "typed"}
	if cursor != "" {
		opts["bookmarks"] = []string{cursor}
	}
	data, err := json.Marshal(map[string]any{"options": opts, "context": map[string]any{}})
	if err != nil {
		return nil, err
	}
	u := "https://www.pinterest.com/resource/BaseSearchResource/get/?" + url.Values{
		"source_url": {"/search/pins/?q=" + url.QueryEscape(q) + "&rs=typed"},
		"data":       {string(data)},
		"_":          {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}.Encode()

	res, err := fetch(ctx, u, map[string]string{
		"Accept":                  "application/json, text/javascript, */*, q=0.01",
		"Referer":                 "https://www.pinterest.com/",
		"X-Requested-With":        "XMLHttpRequest",
		"X-Pinterest-AppState":    "active",
		"X-Pinterest-PWS-Handler": "www/search/[scope].js",
	})
	if err != nil {
		return nil, err
	}
	if res.Status == 429 {
		return nil, fmt.Errorf("Pinterest is rate limiting this server; try again in a minute")
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("Pinterest answered with HTTP %d", res.Status)
	}

	var r pinResponse
	if err := json.Unmarshal(res.Body, &r); err != nil {
		return nil, fmt.Errorf("Pinterest returned an unexpected response")
	}

	out := []Image{}
	for _, p := range r.ResourceResponse.Data.Results {
		orig, ok := p.Images["orig"]
		if !ok || orig.URL == "" {
			continue
		}
		thumb := orig
		for _, k := range []string{"474x", "236x"} {
			if v, ok := p.Images[k]; ok && v.URL != "" {
				thumb = v
				break
			}
		}
		title := strings.TrimSpace(p.GridTitle)
		if title == "" {
			title = strings.TrimSpace(p.Title)
		}
		img := Image{
			ID:     p.ID,
			Thumb:  thumb.URL,
			Full:   orig.URL,
			Title:  title,
			Source: "https://www.pinterest.com/pin/" + p.ID + "/",
			Width:  orig.Width,
			Height: orig.Height,
		}
		if lu, err := url.Parse(p.Link); err == nil && lu.Host != "" {
			img.Meta = strings.TrimPrefix(lu.Host, "www.")
		}
		out = append(out, img)
	}

	next := r.ResourceResponse.Bookmark
	if next == "-end-" || len(out) == 0 {
		next = ""
	}
	return &Page{Results: out, Next: next}, nil
}
