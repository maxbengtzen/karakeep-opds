package main

import (
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// The text functions mimic the Python original (models.py) character for character, since
// summaries and titles show up in the reader's catalog and are tested against the golden files.

// isSpace corresponds to Python's str.isspace() and `\s` in str regexes: Unicode whitespace plus
// the control characters 0x1c–0x1f, which Python counts as whitespace but unicode.IsSpace does not.
func isSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip corresponds to str.strip().
func pyStrip(s string) string {
	return strings.TrimFunc(s, isSpace)
}

// collapseSpace corresponds to re.sub(r"\s+", " ", s).strip().
func collapseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, isSpace), " ")
}

var (
	imgTagRe     = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	anyTagRe     = regexp.MustCompile(`<[^>]+>`)
	mdDataImgRe  = regexp.MustCompile(`(?s)!\[[^\]]*]\(data:[^)]+\)`)
	dataURIRe    = regexp.MustCompile(`data:[^\s\p{Zs}\x{85}\x{2028}\x{2029}\x{1c}-\x{1f})>"]+`)
	maxSummary   = 500
	maxContent   = 4000
	titleExcerpt = 80
)

// htmlToText corresponds to html_to_text: remove images and tags, decode entities, clean up.
func htmlToText(v string) string {
	if pyStrip(v) == "" {
		return ""
	}
	v = imgTagRe.ReplaceAllString(v, " ")
	v = anyTagRe.ReplaceAllString(v, " ")
	return cleanText(html.UnescapeString(v))
}

// cleanText corresponds to clean_text: remove embedded data URIs and collapse whitespace.
func cleanText(v string) string {
	if pyStrip(v) == "" {
		return ""
	}
	v = mdDataImgRe.ReplaceAllString(v, " ")
	v = imgTagRe.ReplaceAllString(v, " ")
	v = dataURIRe.ReplaceAllString(v, " ")
	return collapseSpace(v)
}

// excerpt corresponds to excerpt: collapse and shorten to limit characters (not bytes), with "...".
func excerpt(v string, limit int) string {
	if v == "" {
		return ""
	}
	compact := collapseSpace(v)
	r := []rune(compact)
	if len(r) <= limit {
		return compact
	}
	return strings.TrimRightFunc(string(r[:limit-1]), isSpace) + "..."
}

// firstText returns the first value that is a non-empty string, trimmed.
func firstText(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok {
			if t := pyStrip(s); t != "" {
				return t
			}
		}
	}
	return ""
}

// sameText compares two texts like Python's _same_text (whitespace collapsed, case folded).
func sameText(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(collapseSpace(a), collapseSpace(b))
}

// parseTime corresponds to parse_datetime: ISO 8601 in the variants that
// datetime.fromisoformat handles and Karakeep may send. A time without a zone is read as UTC.
// Anything else gives now.
func parseTime(v any, now time.Time) time.Time {
	s, ok := v.(string)
	if !ok || pyStrip(s) == "" {
		return now
	}
	s = pyStrip(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Truncate(time.Microsecond)
		}
	}
	return now
}

// isoTime formats like Python's datetime.isoformat() in UTC with Z: microseconds only when
// they are not zero.
func isoTime(t time.Time) string {
	t = t.UTC()
	if us := t.Nanosecond() / 1000; us != 0 {
		return t.Format("2006-01-02T15:04:05") + "." + pad6(us) + "Z"
	}
	return t.Format("2006-01-02T15:04:05Z")
}

func pad6(n int) string {
	s := []byte("000000")
	for i := 5; i >= 0 && n > 0; i-- {
		s[i] = byte('0' + n%10)
		n /= 10
	}
	return string(s)
}

// xmlSafe removes characters that are not allowed in XML 1.0. The Python original passed them
// through and produced a broken feed.
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r >= 0xD800 && r <= 0xDFFF:
			return -1
		}
		return r
	}, s)
}

// xmlText escapes text the way ElementTree does.
func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(xmlSafe(s))
}

// xmlAttr escapes attribute values the way ElementTree does.
func xmlAttr(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
		"\r", "&#13;", "\n", "&#10;", "\t", "&#09;",
	).Replace(xmlSafe(s))
}
