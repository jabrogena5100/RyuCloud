# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS base
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

FROM base AS build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Production-style image: a single static binary (migrations are embedded) on a
# minimal non-root base with no shell.
FROM gcr.io/distroless/static-debian12:nonroot AS prod
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8000
ENTRYPOINT ["/api"]
CMD ["serve"]
