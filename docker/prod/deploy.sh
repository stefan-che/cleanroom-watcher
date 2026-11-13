#!/usr/bin/env bash
#
# Roll out fresh scanner code to production.
#
# Deploy model: the server holds a clone of the repository and the image is built
# straight from it. Deploy = git pull + image rebuild + container recreation.
# MySQL, Redis and nginx are left alone; only the scanner has downtime
# (a few seconds; it catches up on missed blocks by itself, starting from
# scanner_state.last_block).
#
# Usage:
#   ./deploy.sh              — git pull + build + restart
#   ./deploy.sh --no-pull    — no git pull (code is already in place)
#   ./deploy.sh --with-infra — also rebuild/restart nginx
#
# Rollback: make rollback (the previous image is kept under the :prev tag).
set -euo pipefail

cd "$(dirname "$(readlink -f "$0")")"

DC="docker compose"
PULL=1
WITH_INFRA=0

for arg in "$@"; do
    case "$arg" in
        --no-pull)    PULL=0 ;;
        --with-infra) WITH_INFRA=1 ;;
        *) echo "Unknown argument: $arg" >&2; exit 2 ;;
    esac
done

log()  { printf '\033[36m[deploy]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[deploy]\033[0m %s\n' "$*" >&2; exit 1; }

# Read a value from .env without sourcing it: passwords may contain anything.
envval() { grep -E "^$1=" .env 2>/dev/null | tail -1 | cut -d= -f2-; }

# --- 0. Checks before changing anything --------------------------------------

log "0/6 checks"
[ -f .env ]        || fail "No .env: copy .env.example and fill it in"
[ -f config.json ] || fail "No config.json: copy config.prod.json.example and fill it in"

APP_PATH="$(envval APP_PATH)";           APP_PATH="${APP_PATH:-../..}"
SCANNER_IMAGE="$(envval SCANNER_IMAGE)"; SCANNER_IMAGE="${SCANNER_IMAGE:-cleanroom-watcher-scanner}"

# Broken JSON guarantees a scanner crash on start.
python3 -c "import json,sys; json.load(open('config.json'))" \
    || fail "config.json does not parse as JSON: deploy stopped"

# --- 1. Fresh code ----------------------------------------------------------

if [ "$PULL" -eq 1 ]; then
    if git -C "$APP_PATH" rev-parse --git-dir >/dev/null 2>&1; then
        log "1/6 git pull in $APP_PATH"
        git -C "$APP_PATH" pull --ff-only
    else
        log "1/6 $APP_PATH is not a git repository, skipping pull"
    fi
else
    log "1/6 git pull skipped (--no-pull)"
fi

# --- 2. Rollback point ------------------------------------------------------

log "2/6 saving the current image as ${SCANNER_IMAGE}:prev"
if docker image inspect "${SCANNER_IMAGE}:prod" >/dev/null 2>&1; then
    docker image tag "${SCANNER_IMAGE}:prod" "${SCANNER_IMAGE}:prev"
else
    log "    no previous image: this is the first build"
fi

# --- 3. Build ---------------------------------------------------------------

log "3/6 building the scanner image"
# The date busts the cache of the `apk upgrade` layer (ARG CACHEBUST in the Dockerfile):
# without it, alpine patches do not reach the image even with --pull.
export CACHEBUST="$(date +%F)"
if [ "$WITH_INFRA" -eq 1 ]; then
    ${DC} build --pull
else
    ${DC} build listener-scanner
fi

# --- 4. Restart -------------------------------------------------------------

# The application applies migrations itself on start (golang-migrate,
# /app/migrations inside the image), so no separate step is needed.
log "4/6 recreating the scanner container"
${DC} up -d --force-recreate --no-deps listener-scanner

if [ "$WITH_INFRA" -eq 1 ]; then
    log "    recreating nginx"
    ${DC} up -d --force-recreate --no-deps listener-nginx
fi

# --- 5. Wait for healthy ----------------------------------------------------

log "5/6 waiting for the scanner to become healthy"
CID="$(${DC} ps -q listener-scanner)"
[ -n "$CID" ] || fail "scanner container did not start, see: make logs-scanner"

for i in $(seq 1 60); do
    status="$(docker inspect -f '{{.State.Health.Status}}' "$CID" 2>/dev/null || echo unknown)"
    case "$status" in
        healthy)   log "    healthy after ${i}0 seconds… ok"; break ;;
        unhealthy) fail "scanner is unhealthy. Logs: make logs-scanner. Rollback: make rollback" ;;
    esac
    if [ "$i" -eq 60 ]; then
        fail "scanner did not come up within 10 minutes. Logs: make logs-scanner. Rollback: make rollback"
    fi
    sleep 10
done

# --- 6. Summary -------------------------------------------------------------

log "6/6 stack status"
${DC} ps --format 'table {{.Name}}\t{{.Status}}'
echo
${DC} exec -T listener-scanner wget -qO- http://127.0.0.1:8080/health \
    | python3 -m json.tool 2>/dev/null || true

log "done"
