# cleanroom-watcher — TRON address watcher with signed webhooks

[![CI](https://github.com/stefan-che/cleanroom-watcher/actions/workflows/ci.yml/badge.svg)](https://github.com/stefan-che/cleanroom-watcher/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Part of the **tron-cleanroom** stack (required component: block scanner) → [overview](https://github.com/stefan-che/tron-cleanroom) · [architecture](https://github.com/stefan-che/tron-cleanroom/blob/main/ARCHITECTURE.md) · [full-stack quickstart](https://github.com/stefan-che/tron-cleanroom/blob/main/docs/QUICKSTART.md). Siblings: [cleanroom-vault](https://github.com/stefan-che/cleanroom-vault) · [tally-bot](https://github.com/stefan-che/tally-bot)

A self-hosted Go service. You register TRON addresses; it reads every block and sends your backend a signed webhook when TRX or USDT (TRC-20) moves on one of them.

**Role in crypto hygiene:** cleanroom-watcher is the eyes of the stack. It detects every incoming transfer to a transit wallet, so AML screening starts the moment funds arrive.

**Status:** Production-proven: built and run in production as the processing stack of WOL, a Dubai OTC desk, 2022–2026.

**Scope:** TRON mainnet, TRX and one configured USDT (TRC-20) contract. Other networks (Bitcoin, Ethereum and ERC-20) via optional Blockchair polling.

## What you get

- Know when money lands on any of your per-payment addresses, without per-address polling.
- One block scan serves all addresses: lookup is an in-process map. Designed for tens of thousands of addresses.
- Events are stored in MySQL before sending, so a slow receiver never slows the scanner. Failed deliveries are retried and visible in a delivery log.
- Node failover across several TRON full nodes / TronGrid; progress advances only over contiguous successfully scanned blocks.
- Addresses expire on their own (TTL); `sync: true` back-fills past transfers for a new address.
- Optional polling of other networks (Bitcoin, Ethereum and ERC-20) through Blockchair, with your own Blockchair key and an optional working-hours window to manage quota.

## How it works

```
TRON full nodes ──► Scanner ──► watch map (RAM) ──hit──► MySQL events ──► Dispatcher + retry ──► your backend
 / TronGrid        (workers)         ▲                                       (HMAC-signed POST)
                                     │
your backend ──► HTTP API (X-API-Key) ┘   Redis (optional): TTL expiry
```

## HTTP API

All endpoints except `/health` need the `X-API-Key` header.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/watch` | start watching (`network`, `address`, `ttl_hours`, `webhooks`, `sync`, `meta`) |
| `POST` | `/api/unwatch` | stop watching |
| `GET` | `/api/addresses`, `/api/addresses/{address}` | list / detail with balance |
| `GET` `POST` `DELETE` | `/api/addresses/{address}/webhooks[/{id}]` | per-address webhooks |
| `GET` | `/api/addresses/{address}/transactions` | stored transfers, paginated |
| `GET` | `/api/transactions` | live lookup via Blockchair / TronScan, not stored |
| `GET` `POST` | `/api/webhooks/deliveries`, `…/{id}/retry` | delivery log, manual retry |
| `GET` | `/health` | DB/Redis status, block lag, pending webhooks (no auth) |

## Webhook contract

`POST` with `Content-Type: application/json`. If the webhook has a `secret`, the request carries:

```
X-Signature: sha256=<hex HMAC-SHA256(raw body, secret)>
```

Payload fields: `network`, `currency`, `txid`, `block`, `ts`, `address`, `from`, `to`, `amount`, `amount_raw`, `fee`, `fee_currency`, `direction` (`IN` or `OUT` relative to `address`). Any `2xx` acknowledges; other answers are retried after 5 s, then 30 s, then 60 s. The default `max_retries: 3` means 3 attempts in total. Delivery is at least once: deduplicate by `txid` on your side. Full example and verification snippet: [docs/WEBHOOKS.md](docs/WEBHOOKS.md).

## Quickstart (Docker Compose)

```bash
make config      # config.json from config.docker.json.example
$EDITOR config.json   # see the checklist below
make up          # creates network processing-net, then docker compose up -d
curl http://127.0.0.1:8081/health
```

Before `make up`, edit `config.json`: set `api.api_keys` to at least one random key (`openssl rand -hex 32`; the key check is enforced once the list is non-empty), set `scanner.start_block` to a recent block (0 on an empty DB scans from genesis), add your `tron_nodes`, and set your own `admin.*` values. Override the `change-me` DB passwords (`LISTENER_DB_PASSWORD`, `LISTENER_DB_ROOT_PASSWORD`) and keep `db_dsn` in sync.

Register an address:

```bash
curl -X POST http://127.0.0.1:8081/api/watch -H 'X-API-Key: <your key>' -H 'Content-Type: application/json' \
  -d '{"network":"tron","address":"T...","ttl_hours":72,"webhooks":[{"url":"https://example.com/hook","secret":"<random secret>"}]}'
```

Production stack (nginx + MySQL + Redis): [docker/prod/README.md](docker/prod/README.md). Without Docker: `go run ./cmd/scanner` (needs MySQL; Redis optional).

## Use it alone

cleanroom-watcher has no dependency on the other repositories. Anything that can call a REST API and receive a JSON POST can use it: register addresses, verify `X-Signature`, deduplicate by `txid`. [tron-cleanroom](https://github.com/stefan-che/tron-cleanroom) and [tally-bot](https://github.com/stefan-che/tally-bot) are just two such clients.

## Docs

[Configuration reference](docs/CONFIGURATION.md) · [Webhooks](docs/WEBHOOKS.md) · [Security](SECURITY.md)

Naming: some internal names are kept for compatibility with existing deployments: the MySQL database `tron_scanner`, the `LISTENER_*` compose variables and the `listener-*` service names in `docker/prod`.

## License

Licensed under the MIT License — see [LICENSE](LICENSE).
