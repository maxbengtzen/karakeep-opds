# Built in the Go image. What runs is only the binary and the CA certificates.
# The Go version is pinned to a patch version: the binary carries the standard library with it, so
# a security fix in Go only reaches production when the image is rebuilt. Dependabot proposes updates.
FROM golang:1.27.4-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/karakeep-opds .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/karakeep-opds /karakeep-opds
USER 1000:1000
EXPOSE 8000
HEALTHCHECK --interval=60s --timeout=5s --start-period=5s CMD ["/karakeep-opds", "healthcheck"]
ENTRYPOINT ["/karakeep-opds"]
