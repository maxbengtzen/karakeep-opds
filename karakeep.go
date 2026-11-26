package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FeedItem is a bookmark as shown in the catalog and turned into an EPUB.
type FeedItem struct {
	ID             string
	Kind           string // "link" or "text"
	Title          string
	Updated        time.Time
	Summary        string
	URL            string
	Content        string // shortened text for the catalog
	Text           string // the text note's original text, with line breaks
	Author         string
	HTMLContent    string
	ContentAssetID string
	ImageAssetID   string
	ImageURL       string
}

// BookmarkPage is one page of bookmarks from Karakeep.
type BookmarkPage struct {
	Items      []FeedItem
	NextCursor string
}

// errNotFound means that Karakeep answered 404.
var errNotFound = errors.New("karakeep: not found")

// Karakeep is a client for Karakeep's REST API.
type Karakeep struct {
	origin   string // for example http://karakeep:3000/api/v1
	token    string
	pageSize int
	http     *http.Client
	now      func() time.Time
}

func (k *Karakeep) ListBookmarks(ctx context.Context, cursor string) (BookmarkPage, error) {
	payload, err := k.getJSON(ctx, "/bookmarks", true, url.Values{"cursor": {cursor}})
	if err != nil {
		return BookmarkPage{}, err
	}
	return normalizePage(payload, k.now()), nil
}

func (k *Karakeep) SearchBookmarks(ctx context.Context, q, cursor string) (BookmarkPage, error) {
	payload, err := k.getJSON(ctx, "/bookmarks/search", true, url.Values{"q": {q}, "cursor": {cursor}})
	if err != nil {
		return BookmarkPage{}, err
	}
	return normalizePage(payload, k.now()), nil
}

// GetBookmark returns nil if the bookmark is not a link or a text.
func (k *Karakeep) GetBookmark(ctx context.Context, id string) (*FeedItem, error) {
	payload, err := k.getJSON(ctx, "/bookmarks/"+url.PathEscape(id), false, nil)
	if err != nil {
		return nil, err
	}
	m, _ := payload.(map[string]any)
	item, ok := normalizeBookmark(m, k.now())
	if !ok {
		return nil, nil
	}
	return &item, nil
}

// GetAsset fetches an asset. Only the media type is returned, without parameters.
func (k *Karakeep) GetAsset(ctx context.Context, id string, maxBytes int64) ([]byte, string, error) {
	resp, err := k.do(ctx, "/assets/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("karakeep: asset %s is larger than %d bytes", id, maxBytes)
	}
	return data, mediaType(resp.Header.Get("Content-Type"), "application/octet-stream"), nil
}

func (k *Karakeep) getJSON(ctx context.Context, path string, withLimit bool, params url.Values) (any, error) {
	q := url.Values{}
	for key, vs := range params {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(key, vs[0])
		}
	}
	if withLimit {
		q.Set("limit", strconv.Itoa(k.pageSize))
	}
	resp, err := k.do(ctx, path, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("karakeep: invalid JSON from %s: %w", path, err)
	}
	return payload, nil
}

func (k *Karakeep) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := k.origin + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Accept", "application/json")
	return k.http.Do(req)
}

func statusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("karakeep: %s answered %s", resp.Request.URL.Path, resp.Status)
	}
	return nil
}

func mediaType(header, fallback string) string {
	if header == "" {
		return fallback
	}
	if mt, _, err := mime.ParseMediaType(header); err == nil {
		return mt
	}
	return strings.TrimSpace(strings.SplitN(header, ";", 2)[0])
}

// normalizePage corresponds to normalize_page.
func normalizePage(payload any, now time.Time) BookmarkPage {
	m, _ := payload.(map[string]any)
	raw := m["bookmarks"]
	if raw == nil {
		raw = m["data"]
	}
	list, _ := raw.([]any)
	page := BookmarkPage{Items: []FeedItem{}}
	for _, b := range list {
		bm, _ := b.(map[string]any)
		if item, ok := normalizeBookmark(bm, now); ok {
			page.Items = append(page.Items, item)
		}
	}
	page.NextCursor, _ = m["nextCursor"].(string)
	return page
}

// normalizeBookmark corresponds to normalize_bookmark. ok is false for types other than link
// and text, and for bookmarks without an id.
func normalizeBookmark(b map[string]any, now time.Time) (FeedItem, bool) {
	if b == nil {
		return FeedItem{}, false
	}
	content, _ := b["content"].(map[string]any)
	if content == nil {
		content = map[string]any{}
	}
	kind, _ := content["type"].(string)
	if kind != "link" && kind != "text" {
		return FeedItem{}, false
	}
	id := pyStr(b["id"])
	if id == "" {
		return FeedItem{}, false
	}
	assets, _ := b["assets"].([]any)

	item := FeedItem{
		ID:   id,
		Kind: kind,
		Updated: parseTime(firstTruthy(
			b["modifiedAt"], b["updatedAt"], content["dateModified"], b["createdAt"],
		), now),
		Summary: excerpt(firstText(
			b["summary"], b["note"], content["description"], content["text"],
			htmlToText(str(content["htmlContent"])),
		), maxSummary),
		HTMLContent:    str(content["htmlContent"]),
		ContentAssetID: firstNonEmpty(pyStr(content["contentAssetId"]), assetID(assets, "linkHtmlContent")),
		ImageAssetID: firstNonEmpty(
			pyStr(content["imageAssetId"]), assetID(assets, "bannerImage"),
			pyStr(content["screenshotAssetId"]), assetID(assets, "screenshot"),
		),
		ImageURL: str(content["imageUrl"]),
	}
	if kind == "link" {
		item.URL = str(content["url"])
		item.Title = firstText(b["title"], content["title"], content["url"], id)
		item.Content = excerpt(cleanText(htmlToText(item.HTMLContent)), maxContent)
		item.Author = firstText(content["author"], content["publisher"])
	} else {
		item.URL = str(content["sourceUrl"])
		item.Text = str(content["text"])
		item.Content = excerpt(cleanText(item.Text), maxContent)
		item.Title = firstText(b["title"], excerpt(item.Content, titleExcerpt), id)
	}
	if item.Title == "" {
		item.Title = id
	}
	return item, true
}

func assetID(assets []any, assetType string) string {
	for _, a := range assets {
		m, _ := a.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["assetType"].(string); t == assetType {
			if id := pyStr(m["id"]); id != "" {
				return id
			}
		}
	}
	return ""
}

// str returns v if it is a string, otherwise "".
func str(v any) string {
	s, _ := v.(string)
	return s
}

// pyStr corresponds to str(v or ""): strings and numbers become text, everything else empty.
func pyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		if t.String() == "0" {
			return ""
		}
		return t.String()
	}
	return ""
}

// firstTruthy corresponds to a or b or c in Python.
func firstTruthy(values ...any) any {
	for _, v := range values {
		switch t := v.(type) {
		case nil:
			continue
		case string:
			if t == "" {
				continue
			}
		case bool:
			if !t {
				continue
			}
		case json.Number:
			if f, err := t.Float64(); err == nil && f == 0 {
				continue
			}
		case []any:
			if len(t) == 0 {
				continue
			}
		case map[string]any:
			if len(t) == 0 {
				continue
			}
		}
		return v
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
