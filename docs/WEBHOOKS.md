# Webhooks

Webhooks are global (`webhooks[]` in `config.json`) or per address (`webhooks` in `POST /api/watch`, or `/api/addresses/{address}/webhooks`). Each event is written to MySQL first, then delivered by a worker pool.

## Request

`POST <your url>`, `Content-Type: application/json`. With a non-empty `secret`:

```
X-Signature: sha256=<hex HMAC-SHA256(raw body, secret)>
```

The signature covers the raw request body. Serve the receiver over HTTPS and deduplicate by `txid` (see below).

## Payload

```json
{
  "network": "tron",
  "currency": "USDT",
  "txid": "4f2a...",
  "block": 72000123,
  "ts": "2026-01-15T10:20:30Z",
  "address": "TWatchedAddress...",
  "from": "TSender...",
  "to": "TWatchedAddress...",
  "amount": "150.000000",
  "amount_raw": "150000000",
  "fee": "",
  "fee_currency": "",
  "direction": "IN"
}
```

`direction` is `IN` or `OUT` relative to `address`. Amounts are decimal strings.

## Delivery and retries

- Any `2xx` response acknowledges. Anything else, or a timeout (`timeout_sec`, default 10 s), counts as a failed attempt.
- Retry delays: 5 s after the first failure, 30 s after the second, 60 s after later ones.
- `max_retries` (default 3) is the total number of attempts, not the number of extra retries.
- Delivery states: `PENDING`, `DONE`, `FAILED`. Inspect with `GET /api/webhooks/deliveries`; re-send with `POST /api/webhooks/deliveries/{id}/retry`.

## Verifying on the receiver

Compute the HMAC over the raw bytes before parsing JSON and compare in constant time. Python:

```python
import hmac, hashlib
expected = "sha256=" + hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, request.headers.get("X-Signature", ""))
```

Then deduplicate by `txid` (plus `address` and `direction` if you watch both sides of a transfer). Delivery is at least once.
