# Production scanner stack on Docker

A configuration separate from the development stack (`../../docker-compose.yml`
in the repository root): its own `docker-compose.yml`, its own images, its own
`.env` and its own `config.json`. The root files are not affected — local
development keeps working as before.

An alternative to bare-metal deployment via PM2 (`ecosystem.config.js`,
`deploy/`): the binary, MySQL, Redis and nginx ship as one stack, and an update
is a single command.

The stack does not terminate TLS — only port 80 is exposed, and https is
provided by a front proxy (HAProxy, Caddy, Cloudflare, etc.). See the
"TLS and front proxy" section.

## Services

| Service            | Purpose                                           | Exposed ports |
|--------------------|---------------------------------------------------|---------------|
| `listener-nginx`   | rate limiting, path filter, IP lists, proxying    | 80            |
| `listener-scanner` | Go scanner: TRON blocks, Blockchair pollers, HTTP API, admin panel | 127.0.0.1:8080 |
| `listener-mysql`   | MySQL 8.4 LTS, data in a host directory           | 127.0.0.1:3306 |
| `listener-redis`   | Redis 7.2 with password, AOF and keyspace notifications | none     |

The service is exposed externally only through nginx. Port `8080` is bound to
loopback — for your backend, if it runs on the same server. The MySQL port is
also loopback-only and is needed just for manual dumps; if you do not need it,
remove the `ports` block of `listener-mysql` and the `dbaccess` network.

## Server requirements

* Docker Engine 24+ with the compose v2 plugin (`docker compose version`).
* 2 CPU / 4 GB RAM minimum (default limits: scanner 1 GB, MySQL 1 GB).
* Port 80 (`HTTP_PORT`) reachable by the front proxy; the stack itself does not
  need to face the internet.
* A clone of this repository on the server — the image is built directly from
  source.
* `python3` on the host — it is used to validate `config.json` in
  `make`/`deploy.sh`.

---

## First deployment

```bash
# 1. Code on the server
git clone <repo> /opt/cleanroom-watcher
cd /opt/cleanroom-watcher/docker/prod

# 2. Stack variables
cp .env.example .env
chmod 600 .env
$EDITOR .env          # domain, DB and Redis passwords, PUID/PGID, ADMIN_PATH

# 3. Application config
cp config.prod.json.example config.json
chmod 600 config.json
$EDITOR config.json   # TRON nodes, db_dsn, redis, api_keys, admin panel, webhooks

# 4. Check, build, start
make init             # check-config + fix-perms + build + up

# 5. Status
make health           # all containers healthy, /health responds

# 6. Front proxy subnet — so that rate limiting sees real client IPs
$EDITOR images/nginx/geo/trusted-proxies.inc
make nginx-reload
```

`make init` is idempotent — it can be run repeatedly.

### Required changes in `config.json`

`config.prod.json.example` mirrors the working `deploy/dev/config.json` (the
same `confirmations`, `scan_step`, polling intervals and `work_hours`), but the
secrets in it are replaced with placeholders. The live values of the TronGrid
and Blockchair keys and the private node address are easiest to take from the
same place — `../../deploy/dev/config.json`.

Inside the docker network the hosts are not `127.0.0.1` but the service names:

```diff
-"db_dsn": "tron:pass@tcp(127.0.0.1:3306)/tron_scanner?parseTime=true&loc=UTC",
+"db_dsn": "tron:<DB_PASSWORD>@tcp(listener-mysql:3306)/tron_scanner?parseTime=true&loc=UTC",

-"redis_addr": "localhost:6379",
+"redis_addr": "listener-redis:6379",
+"redis_password": "<REDIS_PASSWORD from .env>",
```

The values must match `.env`:

| `.env`                | `config.json`                              |
|-----------------------|--------------------------------------------|
| `DB_USERNAME`/`DB_PASSWORD`/`DB_DATABASE` | part of `db_dsn`        |
| `REDIS_PASSWORD`      | `redis_password`                            |
| `ADMIN_PATH`          | `admin.path`                                |

The last one is not cosmetic: nginx lets only known paths through, and it takes
the admin panel path from `ADMIN_PATH`. If they differ, the admin panel returns a
dropped connection. `make check-config` verifies this along with JSON validity.

Also required in `config.json`:

* `scanner.start_block` — a block number close to the current one. It applies
  only on the first start with an empty DB; afterwards the position is taken from
  `scanner_state.last_block`. Leaving `0` on a clean database means scanning from
  the network's first block;

* `api.api_keys` — a non-empty list (`openssl rand -hex 32`), otherwise the API
  is open to everyone;
* `admin.password` and `admin.session_secret` — your own values, not those from
  the example;
* `tron_nodes` — with a TronGrid API key. Without a key a public node allows
  3 rps and the scanner goes into 429 recovery almost immediately (verified, see
  "Troubleshooting").

### PUID / PGID

The scanner writes `logs/YYYY-MM-DD.log` to `./logs/scanner` (the same files are
shown in the "Logs" tab of the admin panel). The container runs as `PUID:PGID`
from `.env` — take the values from the owner of the code directory:

```bash
stat -c '%u %g' /opt/cleanroom-watcher
```

`make fix-perms` creates the missing directories (`logs/`, `data/`, `backups/`)
and hands `logs/scanner` to this user. Without it Docker creates the mount point
as root, the application cannot open the log file and silently continues writing
to stdout only (`docker compose logs`).

---

## Updating (deploying a new version)

```bash
cd /opt/cleanroom-watcher/docker/prod
make deploy
```

`deploy.sh` steps:

1. checks: `.env` exists, `config.json` exists, JSON is valid;
2. `git pull --ff-only` in the repository root;
3. the current image is renamed to `:prev` — the rollback point;
4. a new scanner image is built from source;
5. `up -d --force-recreate --no-deps listener-scanner`;
6. wait for `healthy` (up to 10 minutes) and print `/health`.

MySQL, Redis and nginx are not touched. Downtime is seconds: the scanner catches
up on missed blocks by itself, starting from `scanner_state.last_block`.

Variants:

```bash
make deploy-fast              # without git pull (code already in place)
./deploy.sh --with-infra      # also rebuild and restart nginx
make build-nginx              # nginx only: fresh alpine patches + restart
```

Alpine security patches (`apk upgrade` in the Dockerfile) reach the image only
when the cache of that layer is invalidated: the build is marked with the
`CACHEBUST` argument, to which `deploy.sh` and `make` pass the current date.
`build --pull` alone refreshes only the base image. A regular deployment does
not rebuild nginx, so after a scanner report with nginx findings (`make scan`),
run `make build-nginx`.

**A separate migration step is not needed** — the application applies
migrations itself at startup (`golang-migrate`, the `/app/migrations` directory
inside the image).

### Config-only changes

`config.json` is mounted into the container but read once at startup:

```bash
$EDITOR config.json
make reload-config
```

`reload-config` validates the JSON and `admin.path`, restarts only the scanner
and waits for `healthy`. No image rebuild is needed.

Changes to `.env` are not picked up this way — the variables are read when the
container is created, so `docker compose up -d` is required. Changing
`admin.path` also requires recreating nginx: `docker compose up -d
--force-recreate --no-deps listener-nginx` (envsubst runs at container start;
`nginx -s reload` will not see the new variable).

### Rollback

```bash
make rollback     # restore the :prev image and recreate the container
```

A rollback restores the **code**. If the new version has already applied a
migration, the DB schema stays new — rolling back the schema requires
`make restore FILE=...` from a dump taken before the deployment. Before a
release that includes migrations, run `make backup`.

---

## TLS and front proxy

The stack itself does not terminate TLS — HAProxy in front of it does, and
traffic reaches us over HTTP on `HTTP_PORT`. Certbot has been removed from the
image (git history has it if it needs to be restored).

On the HAProxy side the backend needs no `ssl` and must pass headers through:

```haproxy
server listener <host-ip>:<HTTP_PORT> check
option forwardfor
http-request set-header X-Forwarded-Proto https
option httpchk GET /healthz
http-check expect status 200
```

`/healthz` returns 200 for any `Host`, so it is suitable for the health check.

One step is required on the stack side — add the proxy subnet to
[`images/nginx/geo/trusted-proxies.inc`](images/nginx/geo/trusted-proxies.inc)
and run `make nginx-reload`. Without it nginx sees the proxy address instead of
the client address, and a lot depends on `$remote_addr` here:

* rate-limit keys — all clients fall into one bucket, and a single bot disables
  `/api/` for everyone else;
* IP lists in `access/admin-allow.inc` and `access/health-allow.inc` — they would
  allow either everyone or no one;
* `X-Real-IP` and `X-Forwarded-For`, which are passed to the scanner and to the
  nginx logs.

Spoofing is rejected: `X-Forwarded-For` is honored only from the subnets in this
file, and the header is ignored from all other sources (`real_ip_recursive on` —
nginx takes the first untrusted address from the right).

The Go application sets the admin session cookie without the `Secure` flag, so
the "https outside, http inside" setup does not break the admin panel. The flip
side is that traffic before the front proxy is unencrypted: the admin password,
`X-API-Key` and request bodies are readable at any intermediate node between the
proxy and the stack. Keep them on one trusted network.

### If the service does not open

Check in order where the path breaks:

```bash
make ps                                # nginx healthy, port published
ss -tlnp | grep -E ':(80|8080)\s'      # does the host listen on the port configured in HAProxy
curl -s -o /dev/null -w '%{http_code}\n' -H "Host: <domain>" http://<host-ip>:<port>/health
sudo tcpdump -ni eth0 'tcp port <port>' -c 30
```

If `curl` against the internal address responds but `tcpdump` is silent, nothing
reaches the server and HAProxy needs fixing (wrong backend address/port, backend
DOWN, a firewall between networks). Your own `curl` to the host address goes
through loopback and will not show up in `tcpdump` on `eth0`.

---

## Maintenance

```bash
make ps            # container status
make health        # status + /health response (lag, webhook queue)
make logs          # tail logs of all services
make logs-scanner  # scanner only (container stdout)
make logs-app      # application file log for today
make logs-nginx    # nginx access log
make db-shell      # MySQL client
make redis-cli     # Redis client
make backup        # dump the DB to ./backups
make scan          # scan images for vulnerabilities (trivy)
make nginx-reload  # after editing access lists and trusted-proxies.inc
```

Backup in the host cron:

```cron
0 3 * * * cd /opt/cleanroom-watcher/docker/prod && make backup >> /var/log/listener-backup.log 2>&1
```

Dumps accumulate in `./backups` without limit — set up cleanup of old ones as a
separate cron line:

```cron
30 3 * * * find /opt/cleanroom-watcher/docker/prod/backups -name 'dump-*.sql.gz' -mtime +14 -delete
```

Restore (the scanner is stopped while the dump is loaded):

```bash
make restore FILE=backups/dump-20260818-030000.sql.gz
```

### Who connects where

| Path                | Who                       | Restriction                          |
|---------------------|---------------------------|--------------------------------------|
| `/api/...`          | backend, integrations     | `X-API-Key`, 30 r/s, `?sync=true` — 1 r/s |
| `/health`           | monitoring                | list in `images/nginx/access/health-allow.inc` |
| `/<ADMIN_PATH>/...` | administrators            | list in `images/nginx/access/admin-allow.inc`, login 10 r/min |
| everything else     | —                         | connection dropped (444)             |

The access lists live in `images/nginx/access/` and are mounted into the
container — they can be edited without a rebuild and are applied with
`make nginx-reload`. The default is `allow all;`; for the admin panel it is
recommended to keep only administrator addresses (admin allowlist).

### Backend integration

A backend on the same server connects to the loopback port:

```env
LISTENER_API_URL=http://127.0.0.1:8080
LISTENER_API_KEY=<one of api.api_keys in config.json>
```

A backend on a different server connects through the front proxy domain:

```env
LISTENER_API_URL=https://listener.example.com
```

The scanner sends webhooks itself (outbound connections from the `egress`
network); no nginx configuration is needed for that.

---

## Migrating from the old PM2 setup

```bash
# 1. On the old server: dump
mysqldump -u root -p --single-transaction --quick tron_scanner | gzip > tron_scanner.sql.gz

# 2. Copy the file and config.json to the new server
scp tron_scanner.sql.gz new-server:/opt/cleanroom-watcher/docker/prod/backups/

# 3. On the new one: start the stack WITHOUT the scanner, load the dump, then start the scanner
cd /opt/cleanroom-watcher/docker/prod
docker compose up -d listener-mysql listener-redis
make restore FILE=backups/tron_scanner.sql.gz
make up
make health
```

Redis does not need to be migrated: the scanner restores the TTL of watched
addresses from `watched_addresses.expires_at` at startup.

Stop the old process on the previous server (`pm2 delete cleanroom-watcher`) **before**
starting the new one — two scanners on the same DB would duplicate webhooks.

---

## Security measures

**Network.** Only nginx port 80 is exposed. MySQL and Redis live in the
`backend` network with `internal: true` — it has no route to the internet. The
MySQL port is published strictly on `127.0.0.1` (through a separate `dbaccess`
network without NAT) and is needed only for manual dumps.

The `127.0.0.1` prefix on the port mapping is required: without it docker binds
`0.0.0.0`, and the port becomes visible to the subnet bypassing `ufw` — docker's
rules live in the `DOCKER` chain, which `INPUT` never reaches.

**Scanner port.** `8080` is bound to `127.0.0.1` rather than all interfaces: the
API and admin panel are reachable from outside only through nginx, which provides
rate limiting and path filtering.

**Known paths.** nginx proxies only `/api/`, `/health` and the admin path.
Everything else gets `return 444` (connection dropped), and the request never
reaches the Go application. URL scanning and admin path guessing do not even
yield a response code.

**Foreign Host.** A request with an unknown `Host` is closed at the connection
level.

**IP spoofing protection.** `X-Forwarded-For` and `X-Real-IP` are overwritten
with `$remote_addr` — client-supplied values never reach the application.
`$remote_addr` itself is replaced with the client value from `X-Forwarded-For`
only for the subnets in `images/nginx/geo/trusted-proxies.inc` (empty by
default), so rate limiting and IP lists cannot be bypassed with a custom header.

**Request body is not logged.** `POST /api/watch` carries wallet addresses,
webhook URLs and their secrets.

**Rate limiting.** 30 r/s on `/api/`, 10 attempts per minute on the admin login
form, a separate 1 r/s limit for requests with `?sync=true` (each such request
goes to Blockchair and consumes quota), and a limit on concurrent connections
per address.

**Headers.** `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`,
`Permissions-Policy`. HSTS is not set — it should be set by whoever terminates
TLS.

**Base images.** nginx is `1.29-alpine` with `apk upgrade` instead of
`1.27-bookworm` with certbot: the debian variant pulled in the certbot python
tooling, libxml2, gnutls and krb5 for things not needed here. The scanner is
built with the `golang:1.25` compiler (standard library vulnerabilities are fixed
by changing the toolchain, not the version in `go.mod`) and runs on
`alpine:3.22`. Verification is `make scan` (trivy, `--ignore-unfixed`).

**Secrets.** DB and Redis passwords are in `.env` (`chmod 600`), application
secrets in `config.json` (`chmod 600`), and both are in `.gitignore`.
`config.json` is not baked into the image: the same image can be deployed to
different stacks.

**Unprivileged process.** The scanner runs as `PUID:PGID`, not root.
`config.json` is mounted read-only — the application cannot rewrite its own
secrets.

**MySQL.** `local_infile=0`, `secure_file_priv`, `skip_name_resolve`, no
symbolic links; the application has a separate user, root is for maintenance
only.

**Redis.** A password is required; `FLUSHALL`, `FLUSHDB`, `CONFIG` and
`SHUTDOWN` are disabled by renaming.

### Host-level recommendations

* **Host firewall.** Docker writes rules to iptables bypassing ufw — restrict
  ports in the `DOCKER-USER` chain.
* **Rotation of application file logs.** Container logs are rotated (json-file,
  10 MB × 5), while the application writes `logs/scanner/YYYY-MM-DD.log` per day
  and does not delete old files itself. Clean up with a host cron:
  `find .../logs/scanner -name '*.log' -mtime +30 -delete`.
* **Monitoring.** `/health` returns `lag_blocks` and `webhooks_pending` — it is
  advisable to collect them externally and alert on growth.
* **TLS and headers on the front proxy.** HSTS, protocol versions and ciphers
  are the responsibility of HAProxy.

## Performance measures

* **Multi-stage build**: the final scanner image is 20 MB (alpine + a static
  binary), without the Go toolchain. The dependency layer is cached separately,
  so a code change does not trigger another `go mod download`.
* **`.dockerignore`** in the repository root: `.git`, prebuilt binaries and logs
  (~40 MB) are kept out of the build context.
* **Redis with `notify-keyspace-events Ex`** is set in the config rather than
  applied by a command at startup: expired addresses are removed from the cache
  on an event, not by a per-minute reconciliation with MySQL.
* **`maxmemory-policy noeviction`** instead of `volatile-lru`: address keys have
  a TTL, and memory-based eviction would drop an address from watching **without**
  an `expired` event — the scanner would not know about it.
* **MySQL**: `innodb_buffer_pool_size=512M`, `O_DIRECT`, binlog for PITR, slow
  log with a 2 s threshold, `default_time_zone='+00:00'` (the application runs
  in UTC — `loc=UTC` in the DSN).
* **nginx**: gzip for JSON, keepalive to the scanner, `proxy_read_timeout 300s`
  (historical synchronization calls external APIs and can take long).
* **`worker_processes 2`, not `auto`**: `auto` counts the host's cores, while the
  container has an `NGINX_CPUS` quota. On a many-core server this yields dozens
  of workers on a single core and `sendmsg() failed (109)` on every reload.
* **CPU and memory limits** on every service and docker log rotation.

---

## Troubleshooting

**`level=WARN msg="could not set Redis keyspace events" err="ERR unknown command 'config'"`**
— expected. The application tries to set `notify-keyspace-events` with
`CONFIG SET`, but the `CONFIG` command is disabled in production. The value is
already set in `images/redis/redis.conf`, so the behavior is correct. To verify:
`make redis-cli` → the `CONFIG` command is unavailable, but events work —
expired addresses disappear from `watched_addresses` with `active=0`.

**`level=WARN msg="all tron nodes are down"` and 429 from TronGrid**
— a public node without an API key allows 3 rps, which is not enough for a
single tick with `scan_workers=20`. Fix it with a key in `tron_nodes[].api_key`
or with your own node. After several failures the pool goes into recovery and
pings the nodes every 5 minutes.

**Blockchair returns HTML/404 for BNB or Polygon**
— Blockchair API v2 does not support these networks, although they are enabled
in `deploy/dev/config.json`. If such errors appear in the logs, set
`"enabled": false` for `bnb` and `polygon`; this does not affect TRON, Bitcoin
and Ethereum.

**`lag_blocks` is growing**
— the scanner cannot keep up. Check in order: node availability
(`make logs-scanner`), `scan_step` and `scan_workers` in `config.json`, the
`SCANNER_CPUS` limit.

**The admin panel returns a dropped connection**
— `ADMIN_PATH` in `.env` does not match `admin.path` in `config.json`, or the IP
is not in `access/admin-allow.inc`. Check: `make check-config`, then
`make nginx-reload`.

**`make init` fails: `resolve : lstat .../docker/prod/images/scanner: no such file or directory`**
— the `images/scanner` directory is missing on the server. It was caught by the
`scanner` rule in the root `.gitignore`: a rule without a leading slash ignores
**any** path with that name, not just the built binary in the root. It has been
changed to `/scanner`. Check that the file reached the server:

```bash
git ls-files docker/prod/images/scanner   # should list the Dockerfile
ls docker/prod/images/scanner/Dockerfile
```

If the output is empty, the file has not yet been committed and pushed on the
working machine.

**nginx does not start: `host not found in upstream "listener-scanner:8080"`**
— the scanner container did not start (it failed on the config or on
migrations). First `make logs-scanner`, then `docker compose up -d listener-nginx`.

**Requests arrive from a single IP, rate limiting triggers for everyone at once**
— the front proxy subnet is not set in `images/nginx/geo/trusted-proxies.inc`.
Visible in `make logs-nginx`: the `$remote_addr` field of every line contains the
proxy address. Add the subnet and run `make nginx-reload`.

**The "Logs" tab in the admin panel is empty**
— the application is not writing a log file: the `logs/scanner` directory is
owned by root. Run `make fix-perms` and `docker compose restart listener-scanner`.

**`make backup` produces an empty file**
— the root password does not match: `DB_ROOT_PASSWORD` takes effect only when the
MySQL volume is first created. On an already created database, change the
password with `ALTER USER` in `make db-shell`.
