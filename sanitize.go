package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The article HTML is turned into simple XHTML for the EPUB. The Python original went through
// Markdown (markdownify and then markdown) to get rid of everything but the structure. Here the
// same is done directly: an allowlist of semantic tags is kept without attributes (except links
// and images), scripts and the like are removed with their content, and everything else (div,
// span, section …) is unwrapped.

const maxArticleImages = 5

// parseHTML parses with scripting disabled, so <noscript> is parsed as HTML. That is often
// where the real image is when the page loads images with JavaScript.
func parseHTML(s string) (*html.Node, error) {
	return html.ParseWithOptions(strings.NewReader(s), html.ParseOptionEnableScripting(false))
}

// extractImageURLs corresponds to extract_image_urls: at most five unique http(s) URLs in
// document order, with srcset as a fallback when src is missing.
func extractImageURLs(articleHTML string) []string {
	if articleHTML == "" {
		return nil
	}
	doc, err := parseHTML(articleHTML)
	if err != nil {
		return nil
	}
	var urls []string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			u := bestImageSource(n)
			if (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) && !contains(urls, u) {
				urls = append(urls, u)
			}
			if len(urls) >= maxArticleImages {
				return false
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if !walk(c) {
				return false
			}
		}
		return true
	}
	walk(doc)
	return urls
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}

func bestImageSource(n *html.Node) string {
	if src := attr(n, "src"); src != "" {
		return src
	}
	return bestSrcsetURL(attr(n, "srcset"))
}

var srcsetRe = regexp.MustCompile(`(\S+)(?:\s+(\d+)w)?`)

// bestSrcsetURL corresponds to _best_srcset_url: the largest width wins, equal widths are
// decided by the URL (sorted ascending, the last one wins).
func bestSrcsetURL(srcset string) string {
	type cand struct {
		w   int
		url string
	}
	var cands []cand
	for _, chunk := range strings.Split(srcset, ",") {
		m := srcsetRe.FindStringSubmatch(strings.TrimSpace(chunk))
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[2])
		cands = append(cands, cand{w, m[1]})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].w != cands[j].w {
			return cands[i].w < cands[j].w
		}
		return cands[i].url < cands[j].url
	})
	return cands[len(cands)-1].url
}

// xnode is a sanitized element or a text.
type xnode struct {
	tag      string // "" for text
	text     string
	attrs    [][2]string
	children []*xnode
}

var (
	// Removed together with their content.
	dropTags = map[atom.Atom]bool{
		atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
		atom.Embed: true, atom.Svg: true, atom.Math: true, atom.Head: true, atom.Title: true,
		atom.Template: true, atom.Button: true, atom.Input: true, atom.Select: true,
		atom.Textarea: true, atom.Canvas: true, atom.Video: true, atom.Audio: true,
		atom.Source: true, atom.Track: true, atom.Link: true, atom.Meta: true,
	}
	// Kept (without attributes). b and i become strong and em.
	keepTags = map[string]bool{
		"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"ul": true, "ol": true, "li": true, "blockquote": true, "pre": true, "code": true,
		"em": true, "strong": true, "br": true, "hr": true, "sub": true, "sup": true,
		"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "th": true,
		"td": true, "caption": true, "figure": true, "figcaption": true,
		"dl": true, "dt": true, "dd": true,
	}
	inlineTags = map[string]bool{
		"": true, "a": true, "em": true, "strong": true, "code": true, "br": true,
		"img": true, "sub": true, "sup": true,
	}
	// Removed if they end up empty (no text and no image).
	pruneEmpty = map[string]bool{
		"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"blockquote": true, "figure": true, "figcaption": true, "a": true, "em": true,
		"strong": true, "code": true, "sub": true, "sup": true, "pre": true, "ul": true,
		"ol": true, "dl": true, "caption": true,
	}
	voidTags = map[string]bool{"br": true, "hr": true, "img": true}
)

// sanitizeArticle corresponds to _sanitize_article_html. It returns XHTML (empty if nothing
// remains) and which image paths in the EPUB are used.
func sanitizeArticle(articleHTML string, imageBySource map[string]*epubImage, title string) (string, map[string]bool) {
	used := map[string]bool{}
	if articleHTML == "" {
		return "", used
	}
	doc, err := parseHTML(articleHTML)
	if err != nil {
		return "", used
	}
	body := findBody(doc)
	if body == nil {
		return "", used
	}
	var out []*xnode
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, convert(c, imageBySource, used)...)
	}
	out = prune(out, "body")
	out = wrapInline(out)
	out = removeLeadingTitle(out, title)

	var b strings.Builder
	writeBlocks(&b, out, false)
	return strings.TrimSpace(b.String()), used
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

func convert(n *html.Node, images map[string]*epubImage, used map[string]bool) []*xnode {
	switch n.Type {
	case html.TextNode:
		return []*xnode{{text: n.Data}}
	case html.ElementNode:
	default:
		return nil
	}
	if dropTags[n.DataAtom] || n.Namespace != "" {
		return nil
	}
	children := func() []*xnode {
		var cs []*xnode
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			cs = append(cs, convert(c, images, used)...)
		}
		return cs
	}
	tag := n.Data
	switch tag {
	case "img":
		img := images[bestImageSource(n)]
		if img == nil {
			return nil
		}
		used[img.path] = true
		return []*xnode{{tag: "img", attrs: [][2]string{{"src", img.path}, {"alt", attr(n, "alt")}}}}
	case "a":
		href := strings.TrimSpace(attr(n, "href"))
		lower := strings.ToLower(href)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:") {
			return []*xnode{{tag: "a", attrs: [][2]string{{"href", href}}, children: children()}}
		}
		return children()
	case "b":
		tag = "strong"
	case "i":
		tag = "em"
	}
	if keepTags[tag] {
		return []*xnode{{tag: tag, children: children()}}
	}
	return children()
}

// hasContent is true if the node has visible text or an image.
func hasContent(n *xnode) bool {
	if n.tag == "" {
		return collapseSpace(n.text) != ""
	}
	if n.tag == "img" || n.tag == "hr" {
		return true
	}
	for _, c := range n.children {
		if hasContent(c) {
			return true
		}
	}
	return false
}

// prune removes empty elements, and whitespace between the elements of lists and tables.
func prune(nodes []*xnode, parent string) []*xnode {
	var out []*xnode
	for _, n := range nodes {
		if n.tag == "" && isBlockContainer(parent) && collapseSpace(n.text) == "" {
			continue
		}
		if n.tag != "" {
			n.children = prune(n.children, n.tag)
			if pruneEmpty[n.tag] && !hasContent(n) {
				continue
			}
		}
		// Merge adjacent text (left over when elements between them were removed).
		if n.tag == "" && len(out) > 0 && out[len(out)-1].tag == "" {
			out[len(out)-1] = &xnode{text: out[len(out)-1].text + n.text}
			continue
		}
		out = append(out, n)
	}
	return out
}

// wrapInline wraps running text and inline elements at block level in <p>, as Markdown did in
// the original. This applies to body and blockquote; other blocks (li, td …) may hold running text.
func wrapInline(nodes []*xnode) []*xnode {
	var out []*xnode
	var run []*xnode
	flush := func() {
		p := &xnode{tag: "p", children: run}
		if hasContent(p) {
			out = append(out, p)
		}
		run = nil
	}
	for _, n := range nodes {
		if inlineTags[n.tag] {
			run = append(run, n)
			continue
		}
		flush()
		if n.tag == "blockquote" || n.tag == "figure" {
			n.children = wrapInline(n.children)
			if n.tag == "figure" {
				// figure may hold the image and figcaption directly, without a <p> around the image.
				n.children = unwrapLoneImages(n.children)
			}
		}
		out = append(out, n)
	}
	flush()
	return out
}

func unwrapLoneImages(nodes []*xnode) []*xnode {
	for i, n := range nodes {
		if n.tag == "p" && len(n.children) == 1 && n.children[0].tag == "img" {
			nodes[i] = n.children[0]
		}
	}
	return nodes
}

// removeLeadingTitle corresponds to _remove_leading_duplicate_title.
func removeLeadingTitle(nodes []*xnode, title string) []*xnode {
	for len(nodes) > 0 {
		n := nodes[0]
		if n.tag == "" {
			if collapseSpace(n.text) != "" {
				return nodes
			}
			nodes = nodes[1:]
			continue
		}
		if (n.tag == "h1" || n.tag == "h2" || n.tag == "p") && sameText(joinedText(n), title) {
			nodes = nodes[1:]
			continue
		}
		return nodes
	}
	return nodes
}

// joinedText corresponds to get_text(" ", strip=True).
func joinedText(n *xnode) string {
	var parts []string
	var walk func(*xnode)
	walk = func(x *xnode) {
		if x.tag == "" {
			if t := pyStrip(x.text); t != "" {
				parts = append(parts, t)
			}
			return
		}
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, " ")
}

func isBlockContainer(tag string) bool {
	switch tag {
	case "ul", "ol", "dl", "table", "thead", "tbody", "tfoot", "tr", "blockquote", "figure":
		return true
	}
	return false
}

// writeBlocks writes the nodes with a newline between blocks.
func writeBlocks(b *strings.Builder, nodes []*xnode, inPre bool) {
	for i, n := range nodes {
		if i > 0 && !inlineTags[n.tag] {
			b.WriteByte('\n')
		}
		writeNode(b, n, inPre)
	}
}

func writeNode(b *strings.Builder, n *xnode, inPre bool) {
	if n.tag == "" {
		t := n.text
		if !inPre {
			t = collapseRuns(t)
		}
		b.WriteString(xhtmlEscape(t))
		return
	}
	b.WriteString("<" + n.tag)
	for _, a := range n.attrs {
		b.WriteString(" " + a[0] + `="` + xhtmlAttr(a[1]) + `"`)
	}
	if voidTags[n.tag] {
		b.WriteString("/>")
		return
	}
	b.WriteString(">")
	pre := inPre || n.tag == "pre"
	if isBlockContainer(n.tag) {
		b.WriteByte('\n')
		writeBlocks(b, n.children, pre)
		b.WriteByte('\n')
	} else {
		var inner strings.Builder
		for _, c := range n.children {
			writeNode(&inner, c, pre)
		}
		if pre || inlineTags[n.tag] {
			b.WriteString(inner.String())
		} else {
			b.WriteString(strings.TrimSpace(inner.String()))
		}
	}
	b.WriteString("</" + n.tag + ">")
}

// collapseRuns replaces every run of whitespace with a single space (without trimming), the
// way a reader displays the text anyway.
func collapseRuns(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func xhtmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(xmlSafe(s))
}

func xhtmlAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(xmlSafe(s))
}
