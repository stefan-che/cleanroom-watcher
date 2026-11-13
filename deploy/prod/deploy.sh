#!/usr/bin/env bash
# Deploy cleanroom-watcher to a remote server
# Usage: ./deploy/deploy.sh <dev|prod> [--skip-build]
#
# What it does:
#   1. Builds the Linux binary via Docker
#   2. Uploads the binary, config.json, migrations/ and nginx.conf to the server
#   3. Applies the nginx config and restarts PM2
# -------------------------------------------------------------------
set -euo pipefail

# ── Environment settings ──────────────────────────────────────────────

DEV_USER="root"
DEV_HOST=""           # dev server IP or domain
DEV_PORT="22"
DEV_DEPLOY_PATH="/opt/cleanroom-watcher"
DEV_PM2_APP="cleanroom-watcher"

PROD_USER="root"
PROD_HOST=""          # prod server IP or domain
PROD_PORT="22"
PROD_DEPLOY_PATH="/opt/cleanroom-watcher"
PROD_PM2_APP="cleanroom-watcher"

# ── Colors ────────────────────────────────────────────────────────────
CYAN='\033[0;36m'; GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'
log()  { echo -e "${CYAN}[deploy]${NC} $*"; }
ok()   { echo -e "${GREEN}[ok]${NC}    $*"; }
warn() { echo -e "${YELLOW}[warn]${NC}  $*"; }
fail() { echo -e "${RED}[fail]${NC}  $*" >&2; exit 1; }

# ── Arguments ────────────────────────────────────────────────────────
ENV="${1:-}"
SKIP_BUILD=false

[[ $# -ge 1 ]] && shift
while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-build) SKIP_BUILD=true; shift ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

case "$ENV" in
  dev)
    SERVER_USER="$DEV_USER"; SERVER_HOST="$DEV_HOST"; SERVER_PORT="$DEV_PORT"
    DEPLOY_PATH="$DEV_DEPLOY_PATH"; PM2_APP="$DEV_PM2_APP"
    ENV_DIR="$(dirname "$0")/dev"
    ;;
  prod)
    SERVER_USER="$PROD_USER"; SERVER_HOST="$PROD_HOST"; SERVER_PORT="$PROD_PORT"
    DEPLOY_PATH="$PROD_DEPLOY_PATH"; PM2_APP="$PROD_PM2_APP"
    ENV_DIR="$(dirname "$0")/prod"
    ;;
  *)
    fail "Specify the environment: ./deploy/deploy.sh <dev|prod>"
    ;;
esac

[[ -z "$SERVER_HOST" ]] && fail "${ENV^^}_HOST is not set. Fill in the settings at the top of the script."

# ── Paths ─────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_DIR="$SCRIPT_DIR/$ENV"

CONFIG_FILE="$ENV_DIR/config.json"
NGINX_FILE="$ENV_DIR/nginx.conf"

[[ -f "$CONFIG_FILE" ]] || fail "Not found: $CONFIG_FILE - create it from config.json.example"
[[ -f "$NGINX_FILE"  ]] || fail "Not found: $NGINX_FILE"

SSH_CMD="ssh -p $SERVER_PORT ${SERVER_USER}@${SERVER_HOST}"
SCP_CMD="scp -P $SERVER_PORT"

cd "$ROOT_DIR"

echo ""
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}  cleanroom-watcher → ${ENV} (${SERVER_USER}@${SERVER_HOST})${NC}"
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# ── 1. Build ─────────────────────────────────────────────────────────
if [[ "$SKIP_BUILD" == false ]]; then
  log "Building the Linux binary (Docker)..."
  docker run --rm \
    -v "$ROOT_DIR":/src \
    -w /src \
    golang:1.25-alpine \
    go build -ldflags="-w -s" -o scanner ./cmd/scanner
  ok "Built: $(du -sh scanner | cut -f1)"
else
  warn "Build skipped (--skip-build)"
  [[ -f scanner ]] || fail "Binary 'scanner' not found"
fi

# ── 2. Create directories on the server ────────────────────────────────
log "Preparing directories..."
$SSH_CMD "mkdir -p $DEPLOY_PATH /var/log/cleanroom-watcher"

# ── 3. Upload files ───────────────────────────────────────────────
log "Uploading the binary..."
$SCP_CMD scanner "${SERVER_USER}@${SERVER_HOST}:${DEPLOY_PATH}/scanner.new"

log "Uploading config.json..."
$SCP_CMD "$CONFIG_FILE" "${SERVER_USER}@${SERVER_HOST}:${DEPLOY_PATH}/config.json"

log "Uploading migrations/..."
$SCP_CMD -r migrations/ "${SERVER_USER}@${SERVER_HOST}:${DEPLOY_PATH}/"

log "Uploading nginx.conf..."
$SCP_CMD "$NGINX_FILE" "${SERVER_USER}@${SERVER_HOST}:/tmp/cleanroom-watcher.nginx.conf"

ok "All files uploaded"

# ── 4. Atomic swap + nginx + PM2 ────────────────────────────────
log "Applying on the server..."
$SSH_CMD bash <<REMOTE
  set -e

  # Binary
  [ -f ${DEPLOY_PATH}/scanner ] && cp ${DEPLOY_PATH}/scanner ${DEPLOY_PATH}/scanner.backup || true
  mv ${DEPLOY_PATH}/scanner.new ${DEPLOY_PATH}/scanner
  chmod +x ${DEPLOY_PATH}/scanner

  # Nginx
  cp /tmp/cleanroom-watcher.nginx.conf /etc/nginx/sites-available/cleanroom-watcher
  ln -sf /etc/nginx/sites-available/cleanroom-watcher /etc/nginx/sites-enabled/cleanroom-watcher
  nginx -t && systemctl reload nginx

  # PM2
  cd ${DEPLOY_PATH}
  if pm2 describe ${PM2_APP} > /dev/null 2>&1; then
    pm2 restart ${PM2_APP}
  else
    pm2 startOrReload /opt/cleanroom-watcher/ecosystem.config.js --update-env
    pm2 save
  fi
REMOTE

ok "Done"

# ── 5. Health check ───────────────────────────────────────────────────
log "Health check (waiting 3 s)..."
sleep 3

HEALTH=$($SSH_CMD "curl -sf http://127.0.0.1:8080/health 2>/dev/null || echo '{}'")
if echo "$HEALTH" | grep -q '"status":"ok"'; then
  ok "Service is healthy"
else
  warn "Health check failed. Logs:"
  $SSH_CMD "pm2 logs ${PM2_APP} --lines 15 --nostream 2>/dev/null || true"
fi

echo ""
echo -e "${GREEN}${BOLD}✓ Deploy complete [${ENV}]${NC}"
echo ""
