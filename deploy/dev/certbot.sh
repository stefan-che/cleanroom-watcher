#!/usr/bin/env bash
# Issue / renew the Let's Encrypt SSL certificate for the dev server.
# Run on the server as root:  sudo bash /var/www/listener/certbot.sh
#
# The webroot MUST match location /.well-known/acme-challenge/ in nginx.conf,
# which sets  root /var/www/certbot;  otherwise Let's Encrypt gets a 404.
set -e

DOMAIN="listener.example.com"
EMAIL="admin@example.com"
WEBROOT="/var/www/certbot"

# ACME challenge directory (served by nginx)
mkdir -p "$WEBROOT/.well-known/acme-challenge"

certbot certonly --webroot \
  -w "$WEBROOT" \
  -d "$DOMAIN" \
  --agree-tos \
  --non-interactive \
  --email "$EMAIL"

# dev/nginx.conf points directly at /etc/letsencrypt/live/$DOMAIN/,
# so there is no need to copy certificates to /etc/nginx/ssl; just reload the config.
nginx -t && nginx -s reload

echo "Done. Check: https://$DOMAIN/health"
