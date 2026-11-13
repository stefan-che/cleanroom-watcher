# syntax=docker/dockerfile:1
#
# Production nginx in front of the scanner: rate limiting, path filtering, IP lists.
# TLS is not terminated here: only port 80 is exposed, https is handled by the
# front proxy (see README, section "TLS and front proxy").
#
# Base is alpine, not bookworm: the debian variant pulled in certbot with its whole Python
# stack, libxml2, libxslt, gnutls and krb5 for things not needed here, and
# produced over a hundred CVEs in the scanner. The main project does the same
# (src/docker/prod).

FROM nginx:1.29-alpine

# CACHEBUST (build date from deploy.sh / make) busts the cache of the layer below.
# Without it BuildKit always takes `apk upgrade` from the cache: `build --pull`
# only refreshes the base image, and fresh alpine patches never reach the image.
ARG CACHEBUST=
RUN set -eux; \
    echo "cachebust: ${CACHEBUST}"; \
    apk upgrade --no-cache; \
    rm -f /etc/nginx/conf.d/default.conf

# The stock nginx entrypoint runs files from /etc/nginx/templates through
# envsubst on start, so the domain and the admin path are set via variables
# rather than by editing the config.
COPY ./images/nginx/nginx.conf /etc/nginx/nginx.conf
COPY ./images/nginx/templates/ /etc/nginx/templates/
COPY ./images/nginx/snippets/ /etc/nginx/snippets/
# Access lists and trusted proxy subnets are both baked into the image and mounted
# as a volume: the volume overrides them, but the image still works on its own.
COPY ./images/nginx/access/ /etc/nginx/access/
COPY ./images/nginx/geo/ /etc/nginx/geo/

EXPOSE 80

STOPSIGNAL SIGQUIT

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1

CMD ["nginx", "-g", "daemon off;"]
