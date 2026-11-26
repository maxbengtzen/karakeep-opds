package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// OPDS 1.2 (Atom) is written by hand so that the output is byte for byte the same as the Python
// original's ElementTree output, which the golden tests check.

const (
	typeAtom        = "application/atom+xml"
	typeAtomCatalog = "application/atom+xml;profile=opds-catalog"
	typeOPDS2       = "application/opds+json"
	typeEPUB        = "application/epub+zip"
	relAcquisition  = "http://opds-spec.org/acquisition/open-access"
)

type atomWriter struct{ b strings.Builder }

func (a *atomWriter) open(feedID, title string, updated time.Time) {
	a.b.WriteString("<?xml version='1.0' encoding='utf-8'?>\n")
	a.b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">`)
	a.elem("id", feedID)
	a.elem("title", title)
	a.elem("updated", isoTime(updated))
}

func (a *atomWriter) elem(name, text string) {
	a.b.WriteString("<" + name + ">" + xmlText(text) + "</" + name + ">")
}

func (a *atomWriter) link(rel, href, typ string) {
	a.b.WriteString(`<link rel="` + xmlAttr(rel) + `" href="` + xmlAttr(href) + `" type="` + xmlAttr(typ) + `" />`)
}

func (a *atomWriter) close() string {
	a.b.WriteString("</feed>")
	return a.b.String()
}

func renderOPDS1Catalog(base string, now time.Time) string {
	var a atomWriter
	a.open("karakeep-opds", "Karakeep OPDS", now)
	a.link("self", base+"/opds", typeAtom)
	navEntry(&a, "recent", "Recent bookmarks", base+"/opds/recent", now)
	navEntry(&a, "search", "Search bookmarks", base+"/opds/search?q=", now)
	return a.close()
}

func navEntry(a *atomWriter, id, title, href string, now time.Time) {
	a.b.WriteString("<entry>")
	a.elem("id", "karakeep-opds:"+id)
	a.elem("title", title)
	a.elem("updated", isoTime(now))
	a.link("subsection", href, typeAtomCatalog)
	a.b.WriteString("</entry>")
}

// renderOPDS1Feed: query is nil for feeds without a search.
func renderOPDS1Feed(title string, page BookmarkPage, base, selfPath string, query *string, now time.Time) string {
	updated := now
	if len(page.Items) > 0 {
		updated = page.Items[0].Updated
	}
	q := ""
	if query != nil {
		q = *query
	}
	var a atomWriter
	a.open("karakeep-opds:"+selfPath+":"+q, title, updated)
	a.link("self", feedURL(base, selfPath, query, ""), typeAtom)
	if page.NextCursor != "" {
		a.link("next", feedURL(base, selfPath, query, page.NextCursor), typeAtom)
	}
	for _, it := range page.Items {
		a.b.WriteString("<entry>")
		a.elem("id", "karakeep:bookmark:"+it.ID)
		a.elem("title", it.Title)
		a.elem("updated", isoTime(it.Updated))
		if it.Author != "" {
			a.b.WriteString("<author>")
			a.elem("name", it.Author)
			a.b.WriteString("</author>")
		}
		if it.Summary != "" {
			a.elem("summary", it.Summary)
		}
		if it.URL != "" {
			a.link("alternate", it.URL, "text/html")
		}
		a.link(relAcquisition, epubURL(base, it.ID), typeEPUB)
		if it.Content != "" {
			a.b.WriteString(`<content type="text">` + xmlText(it.Content) + "</content>")
		}
		a.b.WriteString("</entry>")
	}
	return a.close()
}

func epubURL(base, id string) string {
	return base + "/opds/bookmarks/" + id + ".epub"
}

// feedURL corresponds to _feed_url: q is included when present (even if empty), cursor only
// when not empty. The encoding is urlencode (spaces become +).
func feedURL(base, path string, query *string, cursor string) string {
	var params []string
	if query != nil {
		params = append(params, "q="+url.QueryEscape(*query))
	}
	if cursor != "" {
		params = append(params, "cursor="+url.QueryEscape(cursor))
	}
	if len(params) == 0 {
		return base + path
	}
	return base + path + "?" + strings.Join(params, "&")
}

// OPDS 2 (JSON). The field order in the structs is the same as in the original.

type opds2Link struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
	Type string `json:"type"`
}

type opds2Nav struct {
	Title string `json:"title"`
	Href  string `json:"href"`
	Type  string `json:"type"`
	Rel   string `json:"rel"`
}

type opds2Title struct {
	Title string `json:"title"`
}

type opds2Catalog struct {
	Metadata   opds2Title  `json:"metadata"`
	Links      []opds2Link `json:"links"`
	Navigation []opds2Nav  `json:"navigation"`
}

type opds2Author struct {
	Name string `json:"name"`
}

type opds2PubMeta struct {
	Identifier  string        `json:"identifier"`
	Title       string        `json:"title"`
	Modified    string        `json:"modified"`
	Author      []opds2Author `json:"author,omitempty"`
	Description string        `json:"description,omitempty"`
}

type opds2Publication struct {
	Metadata opds2PubMeta `json:"metadata"`
	Links    []opds2Link  `json:"links"`
}

type opds2Feed struct {
	Metadata     opds2Title         `json:"metadata"`
	Links        []opds2Link        `json:"links"`
	Publications []opds2Publication `json:"publications"`
}

func renderOPDS2Catalog(base string) []byte {
	return compactJSON(opds2Catalog{
		Metadata: opds2Title{"Karakeep OPDS"},
		Links:    []opds2Link{{"self", base + "/opds2", typeOPDS2}},
		Navigation: []opds2Nav{
			{"Recent bookmarks", base + "/opds2/recent", typeOPDS2, "current"},
			{"Search bookmarks", base + "/opds2/search?q=", typeOPDS2, "search"},
		},
	})
}

func renderOPDS2Feed(title string, page BookmarkPage, base, selfPath string, query *string) []byte {
	links := []opds2Link{{"self", feedURL(base, selfPath, query, ""), typeOPDS2}}
	if page.NextCursor != "" {
		links = append(links, opds2Link{"next", feedURL(base, selfPath, query, page.NextCursor), typeOPDS2})
	}
	pubs := make([]opds2Publication, 0, len(page.Items))
	for _, it := range page.Items {
		p := opds2Publication{
			Metadata: opds2PubMeta{
				Identifier: "karakeep:bookmark:" + it.ID,
				Title:      it.Title,
				Modified:   isoTime(it.Updated),
			},
			Links: []opds2Link{},
		}
		if it.Author != "" {
			p.Metadata.Author = []opds2Author{{it.Author}}
		}
		p.Metadata.Description = it.Summary
		if p.Metadata.Description == "" && it.Content != "" {
			p.Metadata.Description = excerpt(it.Content, maxSummary)
		}
		if it.URL != "" {
			p.Links = append(p.Links, opds2Link{"alternate", it.URL, "text/html"})
		}
		p.Links = append(p.Links, opds2Link{relAcquisition, epubURL(base, it.ID), typeEPUB})
		pubs = append(pubs, p)
	}
	return compactJSON(opds2Feed{Metadata: opds2Title{title}, Links: links, Publications: pubs})
}

// compactJSON writes like Python's json.dumps(ensure_ascii=False, separators=(",", ":")):
// without HTML escaping and without a trailing newline.
func compactJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // only strings and slices: cannot happen
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
