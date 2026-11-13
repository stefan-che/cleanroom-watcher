# CLAUDE.md — cleanroom-watcher

Guide for developers and AI agents. Product overview: [README.md](README.md).

## What this is
A Go service (module `github.com/stefan-che/cleanroom-watcher`, Go 1.25, ~7.3k lines) that watches a pool of addresses
(50k+) and emits a webhook for every matching transfer. TRON (TRX + USDT TRC-20) is scanned
block by block from full nodes; Bitcoin / Ethereum (+ ERC-20) and other networks are polled via
Blockchair. It is the "detect the payment" part of a self-hosted payment stack:
`../cleanroom-vault` (per-payment wallets, split-key encryption) → **cleanroom-watcher** (detection,
webhooks) → `../tron-cleanroom` (AML, energy rental, sweep to destination wallets). The
processing service registers addresses through `POST /api/watch` and consumes the webhooks.

## Commands
```bash
go build ./... && go vet ./... && go test ./...        # check (in Docker: golang:1.25)
go run ./cmd/scanner                                   # run locally (MySQL required, Redis optional)
make up | down | build | restart | logs | db-shell | db-reset   # dev compose (root docker-compose.yml)
cd docker/prod && make init | deploy | rollback | health | backup # production docker stack
make deploy                                            # bare-metal: deploy/prod/deploy.sh prod (pm2 + nginx)
go run ./cmd/debug-blockchair -network ethereum -address 0x... -key <BLOCKCHAIR_KEY> [-raw]
```
Config: `config.json` in the working dir or the path in `CONFIG_FILE`. Migrations in `migrations/`
are applied on start (`golang-migrate`, path `file://migrations` relative to the working dir).

## Package map
| Path | Role |
|---|---|
| `cmd/scanner/main.go` | wiring: config → MySQL + migrations → Redis → cache → node pool → webhooks → HTTP → scanner → pollers |
| `cmd/debug-blockchair/` | manual Blockchair diagnostics CLI |
| `internal/config/` | `Config`, `BlockchainConfig` (`ERC20Tokens`), `BlockchairConfig` (`WorkHours`), `AdminConfig`; `BlockchainByID()` |
| `internal/tron/` | `address.go` base58check ↔ hex40 (stdlib only); `client.go` node HTTP client + `TronClient`; `pool.go` failover `NodePool`; `types.go` |
| `internal/scanner/` | `scanner.go` tick loop with `scan_workers`; `processor.go` `ProcessBlock`, TRX + TRC-20 parsing, dust filter |
| `internal/cache/` | `WatchCache`: `map[hex40]CacheEntry` under `RWMutex`, Redis TTL + keyspace expiry, MySQL fallback |
| `internal/webhook/` | `Dispatcher` worker pool, `RetryWorker` (5 s tick), `Sender` (HMAC `X-Signature`), `EventPayload` |
| `internal/source/` | poller plugin registry: `Register(name, Factory)`, `New(name, Deps)`, `BlockchainPoller` |
| `internal/blockchair/` | REST client (dashboards, ERC-20, UTXO), `Poller` per chain, `save.go` silent history save, `lookup.go` live formatting |
| `internal/tronscan/` | TronScan TRC-20 history for live lookup only |
| `internal/api/` | stdlib `ServeMux` handlers; `middleware.go` `X-API-Key`; `sync.go` historical sync; `address_webhooks.go` |
| `internal/admin/` | embedded HTML admin UI (`embed.FS`, Tailwind), session-cookie login, enabled when `admin.path` is set |
| `internal/db/` | SQL by area: `watched.go`, `events.go`, `scanner.go`, `db.go` |
| `migrations/` | `001_init` (5 tables), `002_add_sync` (balance, synced_at), `003_log_index_bigint` |
| `docker-compose.yml`, `Dockerfile`, `Makefile` | dev stack |
| `docker/prod/` | production stack: scanner + nginx (rate limits, path filter, admin/health allowlists) + MySQL 8.4 + Redis; see its README |
| `deploy/{dev,prod}/` | bare-metal scripts: build in Docker, scp, supervisor/pm2, nginx, certbot |

## Key abstractions
- **WatchCache** — the hot path. TRON addresses are stored as 40-char lowercase hex; `ContainsAny`
  is a map lookup (~50 ns). Loaded from MySQL on start, TTLs mirrored to Redis; Redis
  `__keyevent@N__:expired` removes an entry and sets `active=0`. Without Redis a 1-minute poll of
  `expires_at` does the same.
- **NodePool** — ordered list of TRON endpoints; on error switches to the next; if all fail,
  enters recovery mode and pings the first node every 5 minutes.
- **Scanner** — every `scan_interval_ms`: `safe = head - confirmations`, range
  `[last+1, min(safe, last+scan_step)]` split across `scan_workers`. `last_block` advances only over
  the contiguous successful prefix; failed tails are re-scanned next tick.
- **Event → delivery** — a hit becomes a `transfer_events` row (unique `(network, txid, log_index)`)
  plus `webhook_deliveries` rows (per-address webhooks, else global `webhooks[]`; `webhooks:false`
  disables). `Dispatcher` sends with `webhook_concurrency` workers; failures get `next_retry_at`
  (5 s / 30 s / 60 s), `RetryWorker` picks due `PENDING` rows; after `max_retries` → `FAILED`.
- **BlockchainPoller** — one goroutine per enabled `blockchains[]` entry whose `source` is a
  registered name (`blockchair`). EVM: native calls + each `erc20_tokens` contract. UTXO: tx hashes →
  details → IN/OUT. Optional `blockchair.work_hours` restricts polling to a UTC window.
- **log_index ranges** (uniqueness per tx): `0` TRX; `0..N` TRC-20 logs; `10000+` TRON IN side when
  both ends are watched; Blockchair ERC-20 event id as-is; `30000+i` UTXO outputs; `40000+i` UTXO inputs.

## Data flow
```
TRON:      NodePool.GetBlock → ProcessBlock → WatchCache.ContainsAny → db.InsertEvent
           → db.CreateWebhookDeliveries → Dispatcher → Sender (POST, X-Signature) → your backend
Blockchair: Poller.syncAddress → dashboard / ERC-20 / UTXO → saveAndDispatch (+ AdjustWatchedBalance)
Watch:     POST /api/watch {network, address, ttl_hours, webhooks, sync, meta}
           → watched_addresses + address_webhooks → WatchCache; sync:true → syncHistoricalTransactions (no webhooks)
Live:      GET /api/transactions → Blockchair (network=ethereum|bitcoin|…) or TronScan (network=trc20); not stored
```
Webhook body = `internal/webhook/payload.go` `EventPayload`:
`network, currency, txid, block, ts, address, from, to, amount, amount_raw, fee, fee_currency, direction (IN|OUT)`.

## HTTP API
All routes except `/health` need `X-API-Key` (values from `api.api_keys`).
`POST /api/watch`, `POST /api/unwatch`, `GET /api/addresses`, `GET /api/addresses/{address}`,
`GET|POST /api/addresses/{address}/webhooks`, `DELETE /api/addresses/{address}/webhooks/{id}`,
`GET /api/addresses/{address}/transactions` (`network, sync, per_page≤200, page, sort, from, to, currency, direction`),
`GET /api/transactions`, `GET /api/webhooks/deliveries`, `POST /api/webhooks/deliveries/{id}/retry`, `GET /health`.

## Database (MySQL)
`scanner_state` (single row, `last_block`) · `watched_addresses` (network, address, hex40, active,
expires_at, balance, synced_at, meta) · `address_webhooks` · `transfer_events` (immutable log) ·
`webhook_deliveries` (`PENDING|DONE|FAILED`). Add schema changes as a new numbered
`migrations/NNN_name.{up,down}.sql` pair; never edit applied migrations.

## Config keys (config.json)
`tron_nodes[{url,api_key}]`, `usdt_contract`, `db_dsn`, `redis_addr`, `redis_password`, `redis_db`,
`scanner{start_block, confirmations, scan_step, scan_workers, scan_interval_ms, watch_default_ttl_hours, min_trx_amount, min_usdt_amount}`,
`api{listen, api_keys[]}`, `webhook_concurrency`, `webhooks[{name,url,secret,timeout_sec,max_retries}]`,
`admin{path, username, password, session_secret}`,
`blockchains[{id,name,source,enabled,native_currency,decimals,explorer_tx_url,poll_interval_sec,erc20_tokens[{contract,symbol,decimals}]}]`,
`blockchair{api_key, poll_interval_sec, request_delay_ms, work_hours{enabled,start_utc,end_utc,work_days[]}}`.
Env: `CONFIG_FILE`. Docker prod `.env` (names): `COMPOSE_PROJECT_NAME, APP_PATH, CONFIG_PATH, PUID, PGID,
APP_DOMAIN, HTTP_PORT, CLIENT_MAX_BODY_SIZE, ADMIN_PATH, SCANNER_HOST_BIND, DB_DATABASE, DB_USERNAME,
DB_PASSWORD, DB_ROOT_PASSWORD, MYSQL_DATA_PATH, MYSQL_HOST_BIND, REDIS_PASSWORD, REDIS_DATA_PATH,
*_CPUS, *_MEMORY, *_IMAGE, TZ`. `ADMIN_PATH` must equal `admin.path`.

## Recipes
**Add an ERC-20 token on a Blockchair chain.** Append `{contract, symbol, decimals}` to that chain's
`erc20_tokens` in `config.json`. No code change. Quota per cycle: `addresses × (1 + len(erc20_tokens))`.

**Add a Blockchair-supported chain.** Add a `blockchains[]` entry with `"source": "blockchair"`,
`id` = Blockchair network slug, `decimals` = base units per coin, `poll_interval_sec`. Make sure
`api.normalizeAddress` accepts the address format for that `network`.

**Add a new TRC-20 token on TRON.** Today the processor knows one contract (`usdt_contract`,
`parseUSDTTransfers` in `internal/scanner/processor.go`). Generalise it: add a list of
`{contract, symbol, decimals}` to `Config`, build a `hex40 → token` map once, match `log.Address`
against it, set `transfer.Token` and scale the dust threshold by token decimals. Keep `log_index`
ranges unchanged.

**Add a new data source / chain provider.** Implement `source.BlockchainPoller` (`Run(ctx)`), call
`source.Register("name", factory)` in `cmd/scanner/main.go` next to `blockchair`, use
`"source": "name"` in `blockchains[]`. Persist via `db.InsertEvent` + `db.CreateWebhookDeliveries`
and dispatch through `webhook.Dispatcher` so retries and the admin UI work unchanged. Pick a
`log_index` range that does not collide with the table above.

**Add an API endpoint.** Register the route in `internal/api/handler.go` behind the API-key
middleware; return JSON with `writeJSON`. If nginx in `docker/prod/images/nginx/templates/app.conf.template`
filters paths, allow the new one there.

**Change webhook payload.** Edit `EventPayload` and both builders (`scanner/processor.go`,
`blockchair` save/dispatch). Keep fields additive — receivers verify the HMAC over the raw body.

## Conventions
- Stdlib first: `net/http` `ServeMux`, `log/slog`, `database/sql`; deps are only the MySQL driver,
  go-redis and golang-migrate.
- One file per concern inside a package; SQL lives in `internal/db`, not in handlers.
- Network ids are lowercase (`tron`, `ethereum`, `bitcoin`, …); TRON addresses are compared as hex40.
- Amounts are decimal strings in payloads (`amount`) plus raw base units (`amount_raw`).
- Comments, logs and admin UI are in English.
