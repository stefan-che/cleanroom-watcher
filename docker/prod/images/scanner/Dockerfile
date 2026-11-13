# syntax=docker/dockerfile:1
#
# Production scanner image. The build context is the repository root
# (see build.context in docker-compose.yml).
#
# Differences from the root Dockerfile:
#   * config.json is NOT baked into the image; it is mounted from docker/prod/config.json,
#     so the same image can be rolled out to different stacks;
#   * the binary is built with -trimpath (no build machine directories in paths);
#   * the process runs as an unprivileged user (user: in compose);
#   * there is a HEALTHCHECK on /health.

# The compiler is newer than go 1.22 from go.mod: the language version comes from go.mod,
# but vulnerabilities in the standard library itself are fixed only by a toolchain upgrade.
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# The dependency layer is cached separately: a code change does not re-run go mod download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled: a static binary that runs on bare alpine.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" -o /out/scanner ./cmd/scanner

# ---------------------------------------------------------------------------

FROM alpine:3.22

# apk upgrade: fresh base image patches (checked by make scan).
# ca-certificates: HTTPS to TronGrid/Blockchair/webhooks; tzdata: TZ from the environment.
# CACHEBUST (build date) busts this layer's cache, otherwise BuildKit takes
# `apk upgrade` from the cache and patches never arrive (see images/nginx/Dockerfile).
ARG CACHEBUST=
RUN set -eux; \
    echo "cachebust: ${CACHEBUST}"; \
    apk upgrade --no-cache; \
    apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/scanner /app/scanner
# golang-migrate reads the migrations on every start, so they must sit next to the binary.
COPY migrations /app/migrations

# The log directory is created in advance and is writable by any UID:
# the container runs as the host PUID/PGID, and ./logs/scanner is mounted here.
RUN mkdir -p /app/logs && chmod 777 /app/logs

# Default for running the image without compose (there the UID/GID is overridden by
# user: "${PUID}:${PGID}"). 1000 is the same UID compose uses by
# default, so permissions on mounted volumes suffice without chown.
USER 1000:1000

ENV CONFIG_FILE=/app/config.json

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health >/dev/null || exit 1

ENTRYPOINT ["/app/scanner"]
