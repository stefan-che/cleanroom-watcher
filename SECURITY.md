# Security

**Reporting a security issue:** use the single channel described in the tron-cleanroom repository: [tron-cleanroom SECURITY.md](https://github.com/stefan-che/tron-cleanroom/blob/main/SECURITY.md). Report privately via [GitHub security advisories](https://github.com/stefan-che/tron-cleanroom/security/advisories/new), not in a public issue.

## Built-in protection

- **Signed webhooks:** every webhook with a `secret` carries `X-Signature: sha256=<HMAC-SHA256(raw body, secret)>`.
- **API keys:** `/api/*` endpoints require the `X-API-Key` header (`/health` is open by design for monitoring).
- **Secret admin path:** the admin UI is served only under a configurable path; its session cookie is HMAC-signed, HttpOnly, SameSite=Lax and expires after 8 hours.
- **Reliable delivery:** events are stored in MySQL before sending; failed deliveries are retried (5 s, 30 s, 60 s) and visible in the delivery log.
- **Network defaults:** Compose publishes the API on `127.0.0.1` only; MySQL and Redis are not published. The production nginx config adds rate limits and IP allow-lists for admin and health.

## Production hardening checklist

- Set `api.api_keys` to at least one random key (`openssl rand -hex 32`).
- Set strong `admin.password` and `admin.session_secret` values and a long random `admin.path`; restrict the admin path by IP in nginx.
- Replace every `change-me` default (DB passwords in compose, example webhook secret) and keep `config.json` out of git.
- Deduplicate webhooks by `txid` on the receiver and verify `X-Signature` in constant time.
- Keep the API on a private network; expose only what your backend needs.
