#!/usr/bin/env bash
# Deploy cleanroom-watcher to the dev server
# Usage: ./deploy/dev/deploy.sh
# Usage: ./deploy/dev/deploy.sh --skip-build

set -euo pipefail

# ── Server settings ────────────────────────────────────────────────
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"   # path to the deploy key
SSH_USER="${SSH_USER:-deploy}"
SSH_HOST="${SSH_HOST:-dev.example.com}"   # dev server IP or domain
SSH_PORT="${SSH_PORT:-22}"
DEPLOY_PATH="/var/www/listener"
APP_NAME="cleanroom-watcher"

# ── Colors ────────────────────────────────────────────────────────────
CYAN='\033[0;36m'; GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'
log()  { echo -e "${CYAN}[deploy]${NC} $*"; }
ok()   { echo -e "${GREEN}[ok]${NC}    $*"; }
warn() { echo -e "${YELLOW}[warn]${NC}  $*"; }
fail() { echo -e "${RED}[fail]${NC}  $*" >&2; exit 1; }

SKIP_BUILD=false
while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-build) SKIP_BUILD=true; shift ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

# ── Paths ─────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"   # repository root
CONFIG_FILE="$SCRIPT_DIR/config.json"
NGINX_FILE="$SCRIPT_DIR/nginx.conf"

[[ -f "$CONFIG_FILE" ]] || fail "Not found: $CONFIG_FILE - create it from config.json.example"
[[ -f "$NGINX_FILE"  ]] || fail "Not found: $NGINX_FILE"

# ── SSH agent: enter the passphrase once ────────────────────────────
eval "$(ssh-agent -s)" > /dev/null
trap 'ssh-agent -k > /dev/null 2>&1' EXIT
ssh-add "$SSH_KEY"

SSH="ssh -p $SSH_PORT $SSH_USER@$SSH_HOST"
SCP="scp -P $SSH_PORT"

cd "$ROOT_DIR"

echo ""
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}  cleanroom-watcher → dev (${SSH_USER}@${SSH_HOST}:${SSH_PORT})${NC}"
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

# ── 2. Upload files to a temp directory ────────────────────────────
# Upload to /tmp (writable by the SSH user); sudo moves the files afterwards
log "Uploading files to the server..."
$SCP scanner          "$SSH_USER@$SSH_HOST:/tmp/scanner.new"
$SCP "$CONFIG_FILE"   "$SSH_USER@$SSH_HOST:/tmp/tron-config.json"
#$SCP "$NGINX_FILE"    "$SSH_USER@$SSH_HOST:/tmp/cleanroom-watcher.nginx.conf"
$SCP "$SCRIPT_DIR/supervisor.conf" "$SSH_USER@$SSH_HOST:/tmp/cleanroom-watcher.supervisor.conf"
$SCP "$SCRIPT_DIR/certbot.sh" "$SSH_USER@$SSH_HOST:/tmp/tron-certbot.sh"
$SCP -r migrations/   "$SSH_USER@$SSH_HOST:/tmp/tron-migrations"
ok "Files uploaded"

# ── 3. Apply on the server (via sudo) ────────────────────────────
log "Applying on the server..."
$SSH bash <<REMOTE
  set -e

  # Prepare directories
  sudo mkdir -p $DEPLOY_PATH $DEPLOY_PATH/migrations /var/log/cleanroom-watcher

  # Binary
  [ -f $DEPLOY_PATH/scanner ] && sudo cp $DEPLOY_PATH/scanner $DEPLOY_PATH/scanner.backup || true
  sudo mv /tmp/scanner.new $DEPLOY_PATH/scanner
  sudo chmod +x $DEPLOY_PATH/scanner

  # Config and migrations
  sudo mv /tmp/tron-config.json $DEPLOY_PATH/config.json
  sudo cp -r /tmp/tron-migrations/. $DEPLOY_PATH/migrations/
  sudo rm -rf /tmp/tron-migrations

  # SSL certificate issuance script (run manually as root when needed)
  sudo mv /tmp/tron-certbot.sh $DEPLOY_PATH/certbot.sh
  sudo chmod +x $DEPLOY_PATH/certbot.sh

  # Supervisor: stop the managed process first
  sudo supervisorctl stop $APP_NAME 2>/dev/null || true

  # Kill ALL scanner processes (manual runs, hung ones, etc.)
  sudo pkill -f "$DEPLOY_PATH/scanner" 2>/dev/null || true
  sleep 1
  # SIGKILL for stubborn ones
  sudo pkill -9 -f "$DEPLOY_PATH/scanner" 2>/dev/null || true

  # Make sure no processes are left
  if pgrep -f "$DEPLOY_PATH/scanner" > /dev/null 2>&1; then
    echo "[warn] scanner processes remain after kill - check manually"
  fi

  # Update the supervisor config and start
  sudo cp /tmp/cleanroom-watcher.supervisor.conf /etc/supervisor/conf.d/cleanroom-watcher.conf
  sudo supervisorctl reread
  sudo supervisorctl update
  sudo supervisorctl start $APP_NAME

  # Nginx
  #sudo cp /tmp/cleanroom-watcher.nginx.conf /etc/nginx/sites-available/cleanroom-watcher
  #sudo ln -sf /etc/nginx/sites-available/cleanroom-watcher /etc/nginx/sites-enabled/cleanroom-watcher
  #sudo nginx -t && sudo systemctl reload nginx
REMOTE

ok "Done"

# ── 4. Health check ───────────────────────────────────────────────────
log "Health check (waiting 3 s)..."
sleep 3

HEALTH=$($SSH "curl -sf http://127.0.0.1:8080/health 2>/dev/null || echo '{}'")
if echo "$HEALTH" | grep -q '"status":"ok"'; then
  ok "Service is healthy"
else
  warn "Health check failed. Logs:"
  $SSH "sudo supervisorctl tail $APP_NAME 2>/dev/null || true"
fi

echo ""
echo -e "${GREEN}${BOLD}✓ Deploy complete [dev]${NC}"
echo ""
