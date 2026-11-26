# karakeep-opds

Serves bookmarks from [Karakeep](https://github.com/karakeep-app/karakeep) as OPDS catalogs, so
they can be browsed and downloaded as EPUB on an e-reader (KOReader, Xteink/CrossPoint and others).

A Go port of [yazdipour/karakeep-opds](https://github.com/yazdipour/karakeep-opds) (Python), with
the same configuration and the same feeds, but an image of about 8 MB instead of 174 MB. See
`testdata/README.md` for the few intentional differences.

## Endpoints

| Path | What |
|---|---|
| `/opds` | OPDS 1.2 catalog (Atom) |
| `/opds/recent?cursor=…` | Most recent bookmarks, page by page |
| `/opds/search?q=…&cursor=…` | Search |
| `/opds2`, `/opds2/recent`, `/opds2/search` | The same in OPDS 2 (JSON) |
| `/opds/bookmarks/<id>.epub` | The bookmark as an EPUB |
| `/healthz` | Health check (no login) |
| `/api/dashboard` | Summary card for a homepage dashboard (only with `DASHBOARD_TOKEN`) |

Everything except `/healthz` and `/api/dashboard` requires Basic authentication with
`OPDS_USERNAME`/`OPDS_PASSWORD`.

`/api/dashboard` returns a small JSON summary: EPUBs downloaded, feed requests and failed Karakeep
requests in the last 24 hours, and a `status` based on a check of Karakeep every minute (`down` when
Karakeep doesn't respond, `warn` when the token is rejected):

```json
{
  "metrics": [{"k": "epub 24 h", "v": 3}, {"k": "feeds 24 h", "v": 41}, {"k": "errors 24 h", "v": 0}],
  "status": "ok"
}
```

The counters live in memory and start over on restart (`foot` then says how long ago). The route
requires `Authorization: Bearer <DASHBOARD_TOKEN>` and does not exist (404) without the variable.

## Configuration

| Variable | Default | |
|---|---|---|
| `KARAKEEP_BASE_URL` | – | Karakeep's URL, for example `http://karakeep:3000` |
| `KARAKEEP_API_TOKEN` | – | API key from Karakeep (Settings → API Keys) |
| `OPDS_USERNAME`, `OPDS_PASSWORD` | – | The login the reader uses |
| `OPDS_PAGE_SIZE` | `50` | Bookmarks per page (1–200) |
| `SERVICE_BASE_URL` | – | Public URL for the links in the feed when behind a proxy. Without it the request's host (and `X-Forwarded-Proto`) is used |
| `KARAKEEP_API_PATH` | `/api/v1` | Path to the API |
| `LISTEN_ADDR` | `:8000` | |
| `DASHBOARD_TOKEN` | – | Token for `/api/dashboard`, at least 16 characters (`openssl rand -hex 32`). Without it the route doesn't exist |

## Running

```yaml
services:
  karakeep-opds:
    image: ghcr.io/maxbengtzen/karakeep-opds:0.3.0
    env_file: .env
    read_only: true
    cap_drop: [ALL]
    ports:
      - "8000:8000"
```

## Development

See `AGENTS.md`.

## License

GPL-3.0-or-later, like the original (see `LICENSE`).
