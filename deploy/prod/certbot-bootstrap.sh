#!/usr/bin/env bash
# Run on the server as root
# Obtains the first SSL certificate via certbot webroot:
#   1. Temporarily enables HTTP-only nginx (no SSL)
#   2. Issues the certificate
#   3. Enables the full HTTPS config
set -e

DOMAIN="listener.example.com"
EMAIL="admin@example.com"
WEBROOT="/var/www/listener"
NGINX_CONF="/etc/nginx/sites-available/cleanroom-watcher"

# ── Temporary HTTP-only config ────────────────────────────────────────
cat > "$NGINX_CONF" <<'EOF'
server {
    listen 80;
    listen [::]:80;
    server_name listener.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/listener;
    }

    location / {
        return 200 'ok';
        add_header Content-Type text/plain;
    }
}
EOF

ln -sf "$NGINX_CONF" /etc/nginx/sites-enabled/cleanroom-watcher
mkdir -p "$WEBROOT"
nginx -t && systemctl reload nginx

echo "Nginx is running on HTTP, requesting the certificate..."

certbot certonly --webroot \
  -w "$WEBROOT" \
  -d "$DOMAIN" \
  --agree-tos \
  --non-interactive \
  --email "$EMAIL"

echo "Certificate obtained. Enabling the HTTPS config..."

# ── Restore the full SSL config ────────────────────────────────
cp "$(dirname "$0")/nginx.conf" "$NGINX_CONF"
nginx -t && systemctl reload nginx

echo "Done. Check: https://$DOMAIN/health"
