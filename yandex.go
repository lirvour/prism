package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Yandex has no public image API, so we read the server-rendered /images/search
// page. Two layouts are handled:
//  1. current layout: JSON in a data-state attribute (initialState.serpList.items.entities)
//  2. legacy layout (what 4get parses): "serp-item" JSON inside data-bem attributes
type yandex struct{}

const jsStr = `"((?:[^"\\]|\\.)*)"`

var (
	reState    = regexp.MustCompile(`data-state=(?:"([^"]*)"|'([^']*)')`)
	reSplit    = regexp.MustCompile(`"serp-item":\s*\{`)
	reImgHref  = regexp.MustCompile(`"img_href":` + jsStr)
	reThumb    = regexp.MustCompile(`"thumb":\{"url":` + jsStr)
	rePreview  = regexp.MustCompile(`"preview":\[\{([^}]*)\}`)
	reURLField = regexp.MustCompile(`"url":` + jsStr)
	reW        = regexp.MustCompile(`"w":(\d+)`)
	reH        = regexp.MustCompile(`"h":(\d+)`)
	reTitle    = regexp.MustCompile(`"title":` + jsStr)
	reDomain   = regexp.MustCompile(`"domain":` + jsStr)
	reTags     = regexp.MustCompile(`<[^>]*>`)
)

func unq(s string) string {
	var out string
	if json.Unmarshal([]byte(`"`+s+`"`), &out) == nil {
		return out
	}
	return s
}

func fixURL(s string) string {
	if strings.HasPrefix(s, "//") {
		return "https:" + s
	}
	return s
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.TrimPrefix(u.Host, "www.")
	}
	return ""
}

// ---- layout 1: data-state ----

type yEntity struct {
	ID      string          `json:"id"`
	Pos     int             `json:"pos"`
	Width   int             `json:"origWidth"`
	Height  int             `json:"origHeight"`
	Alt     string          `json:"alt"`
	OrigURL string          `json:"origUrl"`
	Image   string          `json:"image"`
	Snippet json.RawMessage `json:"snippet"`
}

type yState struct {
	InitialState struct {
		SerpList struct {
			Items struct {
				Entities map[string]json.RawMessage `json:"entities"`
			} `json:"items"`
		} `json:"serpList"`
	} `json:"initialState"`
}

func parseYandexState(body string, page int) []Image {
	var ents []yEntity
	for _, m := range reState.FindAllStringSubmatch(body, -1) {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		if !strings.Contains(raw, "origUrl") {
			continue
		}
		var st yState
		if json.Unmarshal([]byte(html.UnescapeString(raw)), &st) != nil {
			continue
		}
		for key, rm := range st.InitialState.SerpList.Items.Entities {
			var e yEntity
			if json.Unmarshal(rm, &e) != nil || e.OrigURL == "" {
				continue
			}
			if e.ID == "" {
				e.ID = key
			}
			ents = append(ents, e)
		}
	}
	sort.SliceStable(ents, func(i, j int) bool {
		if ents[i].Pos != ents[j].Pos {
			return ents[i].Pos < ents[j].Pos
		}
		return ents[i].ID < ents[j].ID
	})

	seen := map[string]bool{}
	out := []Image{}
	for _, e := range ents {
		full := fixURL(e.OrigURL)
		if seen[full] {
			continue
		}
		seen[full] = true
		img := Image{
			ID:     fmt.Sprintf("y%d-%s", page, e.ID),
			Full:   full,
			Thumb:  fixURL(e.Image),
			Title:  strings.TrimSpace(reTags.ReplaceAllString(e.Alt, "")),
			Width:  e.Width,
			Height: e.Height,
			Meta:   hostOf(full),
		}
		if img.Thumb == "" {
			img.Thumb = full
		}
		var sn struct {
			Title  string `json:"title"`
			URL    string `json:"url"`
			Domain string `json:"domain"`
		}
		if len(e.Snippet) > 0 && json.Unmarshal(e.Snippet, &sn) == nil {
			if img.Title == "" {
				img.Title = strings.TrimSpace(reTags.ReplaceAllString(sn.Title, ""))
			}
			if sn.Domain != "" {
				img.Meta = sn.Domain
			}
			img.Source = fixURL(sn.URL)
		}
		out = append(out, img)
	}
	return out
}

// ---- layout 2: legacy serp-item ----

func parseYandexSerpItems(text string, page int) []Image {
	parts := reSplit.Split(text, -1)
	seen := map[string]bool{}
	out := []Image{}
	if len(parts) < 2 {
		return out
	}
	for i, chunk := range parts[1:] {
		if len(chunk) > 30000 {
			chunk = chunk[:30000]
		}
		m := reImgHref.FindStringSubmatch(chunk)
		if m == nil {
			continue
		}
		full := fixURL(unq(m[1]))
		if full == "" || seen[full] {
			continue
		}
		seen[full] = true
		img := Image{ID: fmt.Sprintf("y%d-%d", page, i), Full: full}
		if pm := rePreview.FindStringSubmatch(chunk); pm != nil {
			if um := reURLField.FindStringSubmatch(pm[1]); um != nil {
				img.Thumb = fixURL(unq(um[1]))
			}
			if wm := reW.FindStringSubmatch(pm[1]); wm != nil {
				img.Width, _ = strconv.Atoi(wm[1])
			}
			if hm := reH.FindStringSubmatch(pm[1]); hm != nil {
				img.Height, _ = strconv.Atoi(hm[1])
			}
		}
		if img.Thumb == "" {
			if tm := reThumb.FindStringSubmatch(chunk); tm != nil {
				img.Thumb = fixURL(unq(tm[1]))
			}
		}
		if img.Thumb == "" {
			img.Thumb = full
		}
		if idx := strings.Index(chunk, `"snippet":{`); idx >= 0 {
			end := idx + 2500
			if end > len(chunk) {
				end = len(chunk)
			}
			sn := chunk[idx:end]
			if tm := reTitle.FindStringSubmatch(sn); tm != nil {
				img.Title = strings.TrimSpace(reTags.ReplaceAllString(unq(tm[1]), ""))
			}
			if dm := reDomain.FindStringSubmatch(sn); dm != nil {
				img.Meta = unq(dm[1])
			}
			if um := reURLField.FindStringSubmatch(sn); um != nil {
				img.Source = fixURL(unq(um[1]))
			}
		}
		out = append(out, img)
	}
	return out
}

func (yandex) Search(ctx context.Context, q, cursor string, _ Options) (*Page, error) {
	page := atoiDefault(cursor, 0)
	u := "https://yandex.com/images/search?" + url.Values{
		"text": {q},
		"p":    {strconv.Itoa(page)},
	}.Encode()

	res, err := fetch(ctx, u, map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Referer":                   "https://yandex.com/images/",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "same-origin",
	})
	if err != nil {
		return nil, err
	}
	if strings.Contains(res.URL, "showcaptcha") || res.Status == 429 {
		return nil, errCaptcha
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("Yandex answered with HTTP %d", res.Status)
	}

	body := string(res.Body)
	out := parseYandexState(body, page)
	if len(out) == 0 {
		out = parseYandexSerpItems(html.UnescapeString(body), page)
	}

	if len(out) == 0 {
		if strings.Contains(body, "CheckboxCaptcha") || strings.Contains(body, "SmartCaptcha") || strings.Contains(body, "showcaptcha") {
			return nil, errCaptcha
		}
		if strings.Contains(body, "EmptySearchResults") {
			return &Page{Results: []Image{}}, nil
		}
		// Unknown layout: keep a copy so the parser can be fixed, and say so.
		dump := filepath.Join(os.TempDir(), "prism-yandex-debug.html")
		_ = os.WriteFile(dump, res.Body, 0o644)
		return nil, fmt.Errorf("Yandex returned a page this scraper could not read (saved to %s)", dump)
	}

	return &Page{Results: out, Next: strconv.Itoa(page + 1)}, nil
}
