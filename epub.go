package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"fmt"
	"io"
	"strings"
	"time"
)

// EPUB 3 with the same files and structure as the Python original (which used EbookLib):
// mimetype, container.xml, content.opf, images, stylesheet, the chapter, toc.ncx and nav.xhtml.

var epubImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
}

func supportedEPUBImage(mediaType string) bool {
	_, ok := epubImageTypes[mediaType]
	return ok
}

type epubImage struct {
	uid, path, mediaType string
	data                 []byte
	sources              []string
}

// sourceImage is a fetched image and the URL it was fetched from (may be empty).
type sourceImage struct {
	data      []byte
	mediaType string
	source    string
}

// epubImages corresponds to _epub_images: filter types and drop duplicates (same type, same
// first 64 bytes and same length). Unlike the original, a duplicate's URL is added to the image
// that is kept, so the article can show it.
func epubImages(images []sourceImage) []*epubImage {
	type key struct {
		mediaType, head string
		size            int
	}
	var out []*epubImage
	seen := map[key]*epubImage{}
	for _, im := range images {
		ext, ok := epubImageTypes[im.mediaType]
		if !ok {
			continue
		}
		head := im.data
		if len(head) > 64 {
			head = head[:64]
		}
		k := key{im.mediaType, string(head), len(im.data)}
		if prev := seen[k]; prev != nil {
			if im.source != "" {
				prev.sources = append(prev.sources, im.source)
			}
			continue
		}
		i := len(out) + 1
		e := &epubImage{
			uid:       fmt.Sprintf("image-%d", i),
			path:      fmt.Sprintf("images/image-%d%s", i, ext),
			mediaType: im.mediaType,
			data:      im.data,
		}
		if im.source != "" {
			e.sources = []string{im.source}
		}
		seen[k] = e
		out = append(out, e)
	}
	return out
}

const bookCSS = "body { font-family: serif; line-height: 1.45; padding: 0 1em; }\n" +
	"h1, h2, h3, h4, h5, h6 { font-family: sans-serif; line-height: 1.2; " +
	"margin-top: 1.2em; margin-bottom: 0.6em; font-weight: bold; }\n" +
	"h1 { font-size: 1.75em; border-bottom: 1px solid #ddd; padding-bottom: 0.3em; }\n" +
	"h2 { font-size: 1.5em; }\n" +
	"h3 { font-size: 1.25em; }\n" +
	"strong, b { font-weight: bold; }\n" +
	"em, i { font-style: italic; }\n" +
	"img { max-width: 100%; height: auto; display: block; margin: 1em auto; }\n" +
	"figure { margin: 1em 0; text-align: center; }\n" +
	"blockquote { border-left: 3px solid #ccc; margin: 1em 0; padding-left: 1em; " +
	"font-style: italic; }\n" +
	"pre { background: #f4f4f4; padding: 1em; overflow-x: auto; font-family: monospace; }\n" +
	"code { font-family: monospace; background: #f4f4f4; padding: 0.1em 0.3em; " +
	"border-radius: 3px; }\n" +
	"a { color: #0056b3; text-decoration: none; }\n" +
	"a:hover { text-decoration: underline; }\n" +
	"hr { border: 0; border-top: 1px solid #ddd; margin: 2em 0; }\n"

const containerXML = `<?xml version="1.0" encoding="utf-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile media-type="application/oebps-package+xml" full-path="EPUB/content.opf"/>
  </rootfiles>
</container>
`

// uuid5URL corresponds to uuid.uuid5(uuid.NAMESPACE_URL, name).
func uuid5URL(name string) string {
	ns := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	h := sha1.New()
	h.Write(ns)
	h.Write([]byte(name))
	u := h.Sum(nil)[:16]
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// renderEPUB corresponds to render_epub.
func renderEPUB(item FeedItem, articleHTML string, images []sourceImage, version string, now time.Time) ([]byte, error) {
	imgs := epubImages(images)
	bySource := map[string]*epubImage{}
	for _, im := range imgs {
		for _, s := range im.sources {
			if _, ok := bySource[s]; !ok {
				bySource[s] = im
			}
		}
	}

	uid := "urn:uuid:" + uuid5URL("karakeep:bookmark:"+item.ID)
	title := xmlText(item.Title)
	author := item.Author
	if author == "" {
		author = "Karakeep"
	}

	var opf strings.Builder
	opf.WriteString("<?xml version='1.0' encoding='utf-8'?>\n")
	opf.WriteString(`<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="id" version="3.0" prefix="rendition: http://www.idpf.org/vocab/rendition/#">` + "\n")
	opf.WriteString(`  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">` + "\n")
	opf.WriteString(`    <meta property="dcterms:modified">` + now.UTC().Format("2006-01-02T15:04:05Z") + "</meta>\n")
	opf.WriteString(`    <meta name="generator" content="karakeep-opds ` + xmlAttr(version) + `"/>` + "\n")
	opf.WriteString(`    <dc:identifier id="id">` + uid + "</dc:identifier>\n")
	opf.WriteString("    <dc:title>" + title + "</dc:title>\n")
	opf.WriteString("    <dc:language>en</dc:language>\n")
	opf.WriteString(`    <dc:creator id="creator">` + xmlText(author) + "</dc:creator>\n")
	opf.WriteString("  </metadata>\n  <manifest>\n")
	for _, im := range imgs {
		opf.WriteString(`    <item href="` + im.path + `" id="` + im.uid + `" media-type="` + im.mediaType + `"/>` + "\n")
	}
	opf.WriteString(`    <item href="style/book.css" id="style" media-type="text/css"/>` + "\n")
	opf.WriteString(`    <item href="content.xhtml" id="chapter_0" media-type="application/xhtml+xml"/>` + "\n")
	opf.WriteString(`    <item href="toc.ncx" id="ncx" media-type="application/x-dtbncx+xml"/>` + "\n")
	opf.WriteString(`    <item href="nav.xhtml" id="nav" media-type="application/xhtml+xml" properties="nav"/>` + "\n")
	opf.WriteString("  </manifest>\n  <spine toc=\"ncx\">\n    <itemref idref=\"nav\"/>\n    <itemref idref=\"chapter_0\"/>\n  </spine>\n</package>\n")

	chapter := "<?xml version='1.0' encoding='utf-8'?>\n<!DOCTYPE html>\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" epub:prefix="z3998: http://www.daisy.org/z3998/2012/vocab/structure/#" lang="en" xml:lang="en">` + "\n" +
		"  <head>\n    <title>" + title + "</title>\n" +
		`    <link href="style/book.css" rel="stylesheet" type="text/css"/>` + "\n  </head>\n" +
		"  <body>" + chapterBody(item, articleHTML, imgs, bySource) + "</body>\n</html>\n"

	ncx := "<?xml version='1.0' encoding='utf-8'?>\n" +
		`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">` + "\n" +
		"  <head>\n" +
		`    <meta content="` + uid + `" name="dtb:uid"/>` + "\n" +
		`    <meta content="0" name="dtb:depth"/>` + "\n" +
		`    <meta content="0" name="dtb:totalPageCount"/>` + "\n" +
		`    <meta content="0" name="dtb:maxPageNumber"/>` + "\n" +
		"  </head>\n  <docTitle>\n    <text>" + title + "</text>\n  </docTitle>\n" +
		"  <navMap>\n    <navPoint id=\"content\">\n      <navLabel>\n        <text>" + title + "</text>\n      </navLabel>\n" +
		"      <content src=\"content.xhtml\"/>\n    </navPoint>\n  </navMap>\n</ncx>\n"

	nav := "<?xml version='1.0' encoding='utf-8'?>\n<!DOCTYPE html>\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en" xml:lang="en">` + "\n" +
		"  <head>\n    <title>" + title + "</title>\n  </head>\n  <body>\n" +
		`    <nav epub:type="toc" id="id" role="doc-toc">` + "\n" +
		"      <h2>" + title + "</h2>\n      <ol>\n        <li>\n" +
		`          <a href="content.xhtml">` + title + "</a>\n" +
		"        </li>\n      </ol>\n    </nav>\n  </body>\n</html>\n"

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	add := func(name string, method uint16, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: now})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	files := []struct {
		name   string
		method uint16
		data   []byte
	}{
		{"mimetype", zip.Store, []byte("application/epub+zip")},
		{"META-INF/container.xml", zip.Deflate, []byte(containerXML)},
		{"EPUB/content.opf", zip.Deflate, []byte(opf.String())},
	}
	for _, im := range imgs {
		files = append(files, struct {
			name   string
			method uint16
			data   []byte
		}{"EPUB/" + im.path, zip.Deflate, im.data})
	}
	files = append(files, []struct {
		name   string
		method uint16
		data   []byte
	}{
		{"EPUB/style/book.css", zip.Deflate, []byte(bookCSS)},
		{"EPUB/content.xhtml", zip.Deflate, []byte(chapter)},
		{"EPUB/toc.ncx", zip.Deflate, []byte(ncx)},
		{"EPUB/nav.xhtml", zip.Deflate, []byte(nav)},
	}...)
	for _, f := range files {
		if err := add(f.name, f.method, f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// chapterBody corresponds to _chapter_body: source link, cover image (unless the article
// already shows it) and the article, or a fallback text.
func chapterBody(item FeedItem, articleHTML string, imgs []*epubImage, bySource map[string]*epubImage) string {
	article, used := sanitizeArticle(articleHTML, bySource, item.Title)
	if article == "" {
		article = fallbackArticle(item)
	}
	source := ""
	if item.URL != "" {
		source = `<p><a href="` + xhtmlAttr(item.URL) + `">Source link</a></p>`
	}
	hero := ""
	if len(imgs) > 0 && !used[imgs[0].path] {
		hero = `<figure><img src="` + imgs[0].path + `" alt=""/></figure>`
	}
	return source + "\n" + hero + "\n" + article
}

// fallbackArticle corresponds to _fallback_article. Text notes use the original text, so the
// paragraphs are kept (the original collapsed everything into one paragraph).
func fallbackArticle(item FeedItem) string {
	text := item.Content
	if item.Kind == "text" && pyStrip(item.Text) != "" {
		text = item.Text
	}
	if text == "" {
		text = item.Summary
	}
	if sameText(text, item.Title) {
		return "<p>No extracted article text is available for this bookmark.</p>"
	}
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		if pyStrip(line) != "" {
			parts = append(parts, "<p>"+xhtmlEscape(cleanText(line))+"</p>")
		}
	}
	if len(parts) == 0 {
		return "<p>No extracted text is available for this bookmark.</p>"
	}
	return strings.Join(parts, "\n")
}
