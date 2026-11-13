# syntax=docker/dockerfile:1
#
# Production MySQL: the official 8.4 LTS image plus two changes that remove
# CVEs unrelated to mysqld itself. The main project does the same
# (src/docker/prod/images/mysql).
#
# 8.4 LTS, not 8.0: the 8.0 branch is end-of-life, 8.0.46 is the last tag, and no new
# builds with updated libraries will ship for it.

FROM mysql:8.4.11

# microdnf update: Oracle Linux security updates released after the image
# was built (openssl, glib2, etc.). CACHEBUST (date from deploy.sh / make)
# busts the layer cache, otherwise updates never arrive; see images/nginx.
#
# mysql-shell (mysqlsh) is a separate tool with its own Python and the
# cryptography/urllib3/pyOpenSSL packages: these are all the image's Python CVEs. Neither
# the image entrypoint nor make backup/restore (mysqldump, mysql) call it.
#
# gosu is a Go binary built on old Go (1.24.6): 1 critical and 21 high in the scanner.
# The entrypoint calls it only when started as root, to drop privileges
# to mysql. The container starts as mysql right away (USER below), so gosu
# is not needed. Precondition: the data directory is owned by 999:999 (make fix-perms).
ARG CACHEBUST=
RUN set -eux; \
    echo "cachebust: ${CACHEBUST}"; \
    microdnf -y update; \
    microdnf -y remove mysql-shell; \
    microdnf clean all; \
    rm -rf /var/cache/yum /var/cache/dnf; \
    rm -f /usr/local/bin/gosu

# UID/GID of the mysql user in the official image.
USER 999:999
