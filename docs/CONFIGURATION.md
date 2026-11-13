# Configuration

Everything is in `config.json` (path overridable with the `CONFIG_FILE` env var). Start from `config.json.example` (bare metal) or `config.docker.json.example` (Docker Compose: DB host `mysql`, Redis host `redis`).

| Key | Meaning |
|---|---|
| `tron_nodes[]` | `{url, api_key}` list, used with failover |
| `usdt_contract` | USDT TRC-20 contract address (one contract only) |
| `db_dsn` | MySQL DSN |
| `redis_addr`, `redis_password`, `redis_db` | Redis (optional) |
| `scanner.start_block` | first block on an empty database (0 = from genesis) |
| `scanner.confirmations` | how many blocks behind the head to stay |
| `scanner.scan_step` / `scan_workers` / `scan_interval_ms` | batch size, parallelism, tick interval |
| `scanner.watch_default_ttl_hours` | default address TTL |
| `scanner.min_trx_amount` / `min_usdt_amount` | ignore dust below these amounts |
| `api.listen`, `api.api_keys[]` | HTTP listener and `X-API-Key` values. **Set at least one random key**: the key check is enforced once the list is non-empty. |
| `webhook_concurrency` | parallel webhook deliveries |
| `webhooks[]` | global webhooks `{name, url, secret, timeout_sec, max_retries}` |
| `admin.{path, username, password, session_secret}` | admin UI (disabled if `path` is empty) |
| `blockchains[]` | optional non-TRON networks: `{id, name, source, enabled, native_currency, decimals, explorer_tx_url, poll_interval_sec, erc20_tokens[]}` |
| `blockchair.{api_key, poll_interval_sec, request_delay_ms, work_hours}` | Blockchair polling; `work_hours` is an optional UTC window to save quota |

Current block number for `scanner.start_block`:

```bash
curl -s -X POST https://api.trongrid.io/wallet/getnowblock
```

## Compose notes

- The image does not contain `config.json`; compose mounts `./config.json` read-only.
- MySQL and Redis are not published to the host. The API is published on `127.0.0.1:${LISTENER_HTTP_PORT:-8081}`.
- DB passwords default to `change-me`: override `LISTENER_DB_PASSWORD` / `LISTENER_DB_ROOT_PASSWORD` and keep `db_dsn` in sync. Migrations run on start.
- Next to [tron-cleanroom](https://github.com/stefan-che/tron-cleanroom) on one host, both stacks join the external docker network `processing-net`: processing calls `http://cleanroom-watcher:8080`, the listener posts to `http://tron-cleanroom/webhook/wallet-listener`. Full walkthrough: the full-stack quickstart in tron-cleanroom (`docs/QUICKSTART.md`).
- Bare metal (pm2/supervisor + nginx + certbot): `deploy/`.

## Behaviour worth knowing

- Webhook `webhooks` field of `POST /api/watch`: omitted or `null` uses the global webhooks; `false` disables webhooks (polling mode); an array sets per-address webhooks.
- Events are deduplicated by the unique key `(network, txid, log_index)`.
- Scanner catches up after downtime from `scanner_state.last_block`.
- Stack: Go 1.25, MySQL 8.4, Redis 7 (optional), `golang-migrate`, `slog`.
