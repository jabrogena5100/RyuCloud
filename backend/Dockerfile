# syntax=docker/dockerfile:1

# One Go toolchain for every stage that compiles or runs Go (dev, worker-dev, build). It must match the
# "go" line in go.mod (1.25.0 is the minimum language version; this is the patch release we build with).
# The runtime stages (prod, worker) contain no Go at all. GOTOOLCHAIN=local below stops Go from silently
# downloading a different toolchain, so a dependency that needs a newer Go fails loudly instead.
ARG GO_VERSION=1.25.14
FROM golang:${GO_VERSION}-alpine AS base
# Static binaries; never auto-download a different Go toolchain.
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
WORKDIR /src

# Development image. docker-compose.yml bind-mounts ./backend over /src and runs
# `go run`, with module and build caches kept in named volumes.
FROM base AS dev
# UID 1000 matches the default first user on Ubuntu, so files written to the
# bind mount (e.g. go.sum from `go mod tidy`) stay owned by you.
RUN adduser -D -u 1000 app \
    && mkdir -p /go/pkg/mod /home/app/.cache/go-build \
    && chown -R app:app /go /home/app /src
ENV GOCACHE=/home/app/.cache/go-build
USER app
COPY --chown=app:app go.mod go.sum* ./
# go.sum doesn't exist until the first `go mod tidy` (see README).
RUN if [ -f go.sum ]; then go mod download; fi
COPY --chown=app:app . .
EXPOSE 8000
CMD ["go", "run", "./cmd/api", "serve"]

# Worker development image: the dev image plus FFmpeg. docker-compose.yml bind-mounts
# ./backend over /src and runs `go run ./cmd/worker`. Alpine's ffmpeg package includes
# libx264 and the native AAC encoder, which is all the worker uses.
FROM dev AS worker-dev
USER root
RUN apk add --no-cache ffmpeg \
    && mkdir -p /var/lib/ryucloud-worker \
    && chown app:app /var/lib/ryucloud-worker
USER app
ENV WORKER_TEMP_DIR=/var/lib/ryucloud-worker

FROM base AS build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
    && go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# Production-style image: a single static binary (migrations are embedded) on a
# minimal non-root base with no shell.
FROM gcr.io/distroless/static-debian12:nonroot AS prod
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8000
ENTRYPOINT ["/api"]
CMD ["serve"]

# Production-style worker image: a static binary plus FFmpeg on a minimal non-root base.
FROM alpine:3.21 AS worker
RUN apk add --no-cache ffmpeg \
    && adduser -D -u 10001 worker \
    && mkdir -p /var/lib/ryucloud-worker \
    && chown worker:worker /var/lib/ryucloud-worker
COPY --from=build /out/worker /worker
USER worker
ENV WORKER_TEMP_DIR=/var/lib/ryucloud-worker
ENTRYPOINT ["/worker"]
