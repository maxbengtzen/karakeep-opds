# Test data

- `fixtures/karakeep/` – invented responses from Karakeep's API (bookmarks, article HTML, small
  images). No real bookmarks. The text is partly Swedish on purpose, to test non-ASCII text.
- `fixtures/routes.json` – which file the fake Karakeep answers with for each path and query
  string, and which remote images exist.
- `fixtures/cases.json` – the requests under test.
- `golden/` – the Python original's responses (`ghcr.io/yazdipour/karakeep-opds:0.0.3`) to the same
  requests, recorded with `scripts/golden/record.sh`. They are the contract: the OPDS feeds must be
  byte for byte identical. Only re-record when the fixtures change on purpose.
- `golden-go/` – the Go version's EPUB chapters (`content.xhtml`), reviewed by hand. Updated with
  `go test -run Golden -update`. Review the diff before committing it.

## Intentional differences from the original

In `knownDiffs` in `golden_test.go`:

| Case | Original | Go version |
|---|---|---|
| Wrong password | 401 without `WWW-Authenticate` | 401 with `WWW-Authenticate: Basic`, so the reader prompts again |
| Bookmark that doesn't exist | 502 | 404 `Bookmark not available as EPUB` |
| Empty search (`?q=`, where the catalog's search link points) | asks Karakeep, which answers 400 → 502 | empty list, no request |

In the EPUB (only `content.xhtml` differs; the other files are compared byte for byte, except
`dcterms:modified` and `generator` in `content.opf`):

- The original cleaned the HTML by converting it to Markdown and back. The Go version cleans it
  directly with an allowlist of tags. The result is nearly the same, but without the Markdown
  leftovers (stray `*`, `[text](url)`) the original sometimes left behind.
- The page's `<title>` ended up as a paragraph at the top in the original. It is removed.
- Images with only `srcset` were fetched but not shown in the original. Now they are shown.
- `figure`/`figcaption` are kept instead of becoming paragraphs. Tabs in code blocks are kept.
- Links are only kept for `http`, `https` and `mailto` (relative and `javascript:` links are
  unwrapped).
- Text notes keep their paragraphs (the original merged everything into one paragraph).
- An image that appears twice with different URLs is shown in both places.

Compared against a real Karakeep instance before switching (50 bookmarks): all OPDS feeds were
identical, and the EPUBs had the same text and images apart from the differences above.
