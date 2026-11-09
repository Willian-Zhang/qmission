# syntax=docker/dockerfile:1
# Multi-arch: docker buildx build --platform linux/amd64,linux/arm64 .
# Needs BuildKit (default in Docker 23+, docker compose v2 and the macOS `container` CLI).

ARG GO_VERSION=1.23

# The builder runs on the build host's platform and cross-compiles (pure Go, no cgo),
# so a multi-arch build doesn't run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/qmission ./cmd/qmission

# alpine rather than distroless/scratch: busybox `wget` for the healthcheck,
# tzdata so TZ (e.g. Europe/Berlin) works.
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 qmission \
    && adduser -S -D -H -u 10001 -G qmission qmission
COPY --from=build /out/qmission /usr/local/bin/qmission
# The binary serves `web/` relative to the working directory at `/`. The repo has no
# `web/` today, so nothing is copied and `/` answers 404.
WORKDIR /app
USER qmission:qmission
EXPOSE 9091
ENTRYPOINT ["/usr/local/bin/qmission"]
CMD ["-p", "9091"]
