# Changelog

All changes noticeable to whoever runs the service. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/). How a version is released is described in
[AGENTS.md](AGENTS.md).

## [Unreleased]

### Changed

- All runtime text is now in English: log messages, configuration errors and the
  `/api/dashboard` card (metrics `epub 24 h`, `feeds 24 h`, `errors 24 h`, and the status messages
  and footer). The JSON fields are unchanged.

## [0.2.0] - 2026-11-16

### Added

- `GET /api/dashboard` for a homepage dashboard: EPUBs downloaded, feed requests and errors in the
  last 24 hours, and whether Karakeep responds (checked every minute). Requires the new, optional
  `DASHBOARD_TOKEN` as a bearer token; without it the route doesn't exist.

## [0.1.0] - 2026-11-12

### Added

- First version: Go port of [yazdipour/karakeep-opds](https://github.com/yazdipour/karakeep-opds)
  0.0.3 with the same environment variables, port (8000) and paths (`/opds`, `/opds2`, `/healthz`,
  `/opds/bookmarks/<id>.epub`). The OPDS feeds are byte for byte the same as the original's.
- `karakeep-opds healthcheck` for Docker's health check (included in the image).
- `LISTEN_ADDR` (default `:8000`).

### Changed

- Image of about 8 MB (the original is 174 MB) and around 10–20 MB of memory.
- The EPUBs' HTML is cleaned directly instead of via Markdown: no Markdown leftovers, the page
  title no longer ends up in the text, images with only `srcset` are shown, and text notes keep
  their paragraphs.
- The article's images are fetched concurrently, so EPUBs are built faster.
- A wrong password answers with `WWW-Authenticate: Basic`, so the reader prompts again.
- A bookmark that doesn't exist gives 404 instead of 502.

### Fixed

- The catalog's search link (`/opds/search?q=`) gave 502, since Karakeep doesn't accept an empty
  search. It now gives an empty list.

### Security

- Images are only fetched from public addresses, and the address is checked when the connection
  is opened (which also protects against DNS rebinding). Internal networks such as CGNAT
  (`100.64.0.0/10`) are blocked too.
