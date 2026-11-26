# Instructions for coding assistants

This file is written for you, an LLM or coding assistant helping with this repository. Read all of
it before changing anything. Commits, code comments, CHANGELOG and all runtime text (logs, error
messages, the dashboard card) are written in English (commit subjects follow Conventional Commits).

If the file `AGENTS.local.md` exists in the checkout, read it too. It describes the owner's own
deployment and is not version controlled.

## What it is and where things are

`karakeep-opds` serves Karakeep bookmarks as OPDS catalogs and builds EPUBs from them. It is a Go
port of the Python project `yazdipour/karakeep-opds` 0.0.3, and therefore GPL-3.0-or-later.

| What | Where |
|---|---|
| Code | `main.go` (configuration, startup, `healthcheck`), `server.go` (HTTP), `karakeep.go` (API client and bookmark parsing), `opds.go` (OPDS 1.2 and 2), `epub.go` (EPUB), `sanitize.go` (cleaning article HTML), `images.go` (safe image fetching), `text.go` (text functions that mimic Python), `dashboard.go` (`/api/dashboard`) |
| Tests | `golden_test.go` (against the Python original's responses), `unit_test.go`, `dashboard_test.go`. See `testdata/README.md` |
| Golden files | `testdata/golden/` is recorded with `scripts/golden/record.sh` (runs the original's image). Don't re-record without a reason |
| Image | `ghcr.io/maxbengtzen/karakeep-opds`, built by GitHub Actions on `vX.Y.Z` tags |
| Version | `VERSION` |
| Release scripts | `scripts/release.sh` (cuts a release) and `scripts/changelog.sh` (called by `release.sh` and by GitHub Actions) |
| CI | `.github/workflows/publish.yml` (test, publish), `.github/workflows/govulncheck.yml` |

## Rules

- **No deployment details in the repository.** Paths, host names, networks and anything else about
  a particular server belong in `AGENTS.local.md` or in the deployment's own compose file, never in
  the repository or a commit.
- **No real bookmarks in the repository.** Fixtures are invented; the text is partly Swedish on
  purpose, to test non-ASCII text.
- **Secrets:** no tokens or passwords in code, tests, commits, logs or CHANGELOG. Tests use invented
  values.
- **Ask the owner before anything visible outside the repository:** pushing a version tag (which
  publishes an image and a GitHub Release) and deploying.
- **Never reuse a version number.** Never move or delete a published tag.
- **Keep dependencies minimal.** The only dependency beyond the standard library is
  `golang.org/x/net/html`. Nothing that requires cgo (`CGO_ENABLED=0`).
- **The container runs read-only** as 1000:1000 in a `scratch` image. The code must not write to
  disk.
- **The OPDS feeds must be byte for byte the same as the original's.** An intentional difference
  goes in `knownDiffs` in `golden_test.go` and in `testdata/README.md`.
- **`/api/dashboard` requires `DASHBOARD_TOKEN`.** The service is often exposed without SSO (e-readers
  can't handle it), so the route must never answer without the token. It is not part of the
  original's interface.
- **Image fetching is an SSRF protection** (`images.go`): the address is checked in the dialer's
  `Control`, after DNS. Don't change it to a check before connecting.

## Developing and testing

Without Go installed, run it in the Go image:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27-alpine \
  sh -c 'gofmt -l . && go vet ./... && go test ./...'
```

Race detector: the same command with `golang:1.27`, `-e CGO_ENABLED=1` and `go test -race ./...`.

If the EPUB chapter changes on purpose: `go test -run Golden -update ./...`, and review the diff in
`testdata/golden-go/`.

### Trying it against a real Karakeep

Build the image and start it next to the running instance, in the same Docker network, with the
same environment (see `AGENTS.local.md` for the owner's setup):

```sh
docker build -t karakeep-opds:test --build-arg VERSION=test .
docker run -d --name karakeep-opds-test --read-only --network <network> --env-file <env> karakeep-opds:test
# compare http://karakeep-opds-test:8000 with the running instance from inside the network
docker rm -f karakeep-opds-test
```

Note that `docker run --env-file` does not strip trailing `# comments` from values the way compose
does.

## Commits

Conventional Commits with an English subject: `feat: …`, `fix: …`, `docs: …`, `chore: …`,
`test: …`, `refactor: …`. Release commits are created by `scripts/release.sh`
(`chore(release): vX.Y.Z`); don't write them by hand.

## Versioning and release notes

| Type | When |
|---|---|
| Major | The compose file or the reader's settings must change (a variable is renamed, paths change) |
| Minor | New backwards-compatible feature (new optional variable, OpenSearch …) |
| Patch | Fix, dependency, Go version |

Release notes are written as you go in `CHANGELOG.md` under `## [Unreleased]`, in the same commit
as the change, for whoever runs the service. Use the headings `### Added`, `### Changed`,
`### Fixed`, `### Removed` and `### Security`, in that order, only the ones needed.

## Cutting a release

1. Check that everything since the previous version is under `## [Unreleased]`
   (`git log --oneline v$(cat VERSION)..HEAD`).
2. Choose the version and **ask the owner** before continuing.
3. `scripts/release.sh patch` (or `minor` / `major` / an exact number). It checks the working tree
   and `Unreleased`, runs the tests (in the Go image if Go is missing), writes `VERSION`, moves
   `Unreleased` to the new version, commits and creates an annotated tag.
4. `git push --follow-tags`. `publish.yml` checks that the tag matches `VERSION`, publishes
   `ghcr.io/maxbengtzen/karakeep-opds:X.Y.Z` and creates a GitHub Release from `CHANGELOG.md`.
5. Deploy as described in `AGENTS.local.md`, if it exists, and ask the owner first.
