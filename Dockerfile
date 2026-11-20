# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS reader-build
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl python3 tar \
    && rm -rf /var/lib/apt/lists/* \
    && corepack enable
COPY scripts/build-reader.sh ./scripts/build-reader.sh
COPY web/reader ./web/reader
COPY internal/httpserver/static/ui/mangaalpha-icon.png ./internal/httpserver/static/ui/mangaalpha-icon.png
COPY internal/httpserver/static/ui/mangaalpha-icon-dark.png ./internal/httpserver/static/ui/mangaalpha-icon-dark.png
RUN mkdir -p internal/httpserver/static/reader_dist \
    && touch internal/httpserver/static/reader_dist/placeholder.txt \
    && bash scripts/build-reader.sh

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY --from=reader-build /src/internal/httpserver/static/reader_dist ./internal/httpserver/static/reader_dist

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -X github.com/xJogger/fake-komga-115/internal/buildinfo.Version=${VERSION}" \
      -o /out/fake-komga-115 \
      ./cmd/server

FROM alpine:3.22

ARG VERSION=dev
LABEL org.opencontainers.image.title="MangaAlpha" \
      org.opencontainers.image.description="Komga-compatible manga server for 115 Open API, Local directories, and WebDAV with built-in bookshelf, reader, and Bangumi metadata" \
      org.opencontainers.image.source="https://github.com/xJogger/MangaAlpha" \
      org.opencontainers.image.licenses="MIT AND MPL-2.0" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S fakekomga \
    && adduser -S -D -H -h /nonexistent -G fakekomga -u 10001 fakekomga \
    && mkdir -p /data \
    && chown -R fakekomga:fakekomga /data

COPY --from=build /out/fake-komga-115 /usr/local/bin/fake-komga-115

ENV FK115_HOST=0.0.0.0 \
    FK115_PORT=25600 \
    FK115_DATA_DIR=/data \
    FK115_OPEN_BROWSER=false

EXPOSE 25600
VOLUME ["/data"]
USER fakekomga

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${FK115_PORT:-25600}/health" >/dev/null || exit 1

ENTRYPOINT ["fake-komga-115"]
