# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Nothing yet.

### Known limitations

Tracked parity and reliability items carried into the next release; they are
listed here because they are visible to an operator, not because they are
planned work-in-progress:

- The engine honours only `isActive`, `chatwootInboxId` and `ignoreGroups` per
  connection. `signMessages`, `autoReadMessages`, `rejectCalls` and the
  `inbox*` toggles are editable through the admin surface but are not enforced
  by the engine yet — enabling them has no effect.
- `GET /api/v1/connections/{id}/webhook-status` reports
  `instanceStatus: "unknown"` against Evolution GO 0.7.2's `{"data":{...}}`
  status shape.
- Recovery is reconciliation-only: a send whose outcome cannot be established is
  quarantined as `unconfirmed_send` and never replayed (ADR-0004). Lossless
  recovery of provably unstarted work is still an open design gate.
- Loop-break depends on the message id returned by the Chatwoot write.
- Run **one replica**. Admin sessions carry a per-process nonce and the worker
  queue is in-process; the deployment artifacts pin `replicas: 1`.

## [1.0.0] - 2026-10-13

First public release: the Go rewrite of the bridge, deployed and verified as the
live bridge against Evolution GO 0.7.2 and Chatwoot v4.12.1 (account API inbox),
sharing a MySQL database with the historical Node bridge.

### Added

- **Bidirectional bridge engine** (`internal/bridge`) — Evolution GO →
  Chatwoot and Chatwoot → WhatsApp, both directions covered by the routing
  decision matrix and its regression tests. Previously-unverified legacy
  behaviours were re-derived from the upstream sources and pinned as fixtures.
- **Persistent idempotency** (`internal/dedup`, `processed_messages`) — every
  webhook delivery is claimed durably *before* the acknowledgement, so a
  redelivered event is answered `duplicate` with no upstream write. Exact
  collation keys; a second delivery wins nothing.
- **Quarantine instead of replay** (ADR-0004) — a send whose result cannot be
  established is terminal (`unconfirmed_send`); a crash between accepted send
  and result produces zero extra sends on restart, and stale claims are
  reconciled at boot.
- **Identity integrity** — mapping-first resolution on every inbound path:
  an externally sent message reuses the existing contact/conversation instead of
  creating one; groups are keyed by their stable JID, never by display name;
  contact, group and conversation titles follow their own rules; a bridge-authored
  message is never re-sent in a loop (both directions).
- **Bounded worker pool** (`internal/worker`, ADR-0005) — fixed pool, byte and
  job budgets, graceful drain on `SIGTERM`, `503` backpressure when the queue is
  full, and pre-admission release only.
- **Admin REST API** (`internal/httpapi`) — 32 routes under `/api/v1/*`
  (auth/session, dashboard, connections, Chatwoot inboxes and settings,
  notifications, logs, settings) with session-cookie authentication, bcrypt
  cost 12, login rate limiting, CSRF (`X-CSRF: 1` plus a JSON content type),
  exact-origin CORS, body caps and pagination bounds. The route table is shared
  with a bidirectional OpenAPI coverage test.
- **Independent React SPA** (`frontend/`) — React 19 + Vite + Tailwind +
  shadcn/ui, serving the same UI over a typed REST client (the tRPC and
  superjson client is gone). Runtime configuration is injected as
  `/config.js`, so one image can be pointed at any bridge origin. The backend
  never serves assets (ADR-0007).
- **Forward-only migrations** (ADR-0002) — `0001`-`0004`, additive and
  idempotent (`IF NOT EXISTS`), `CREATE`-only, never touching the historical
  migration bookkeeping table; applied under an advisory lock.
- **`bridge migrate` and `bridge healthcheck` subcommands** — the server does
  not migrate at boot, so the migration is an explicit one-shot step before the
  first start; `healthcheck` is the container's liveness probe.
- **Structured logging with a payload sanitizer**
  (`internal/observability`) — `log/slog` JSON to stdout, with one choke point
  that strips tokens, auth headers, cookies, media base64 and message content;
  webhook path tokens are masked (byte-length preserving) in every log line.
- **Deployment artifacts** (`deploy/`) — a generic Compose file (non-root, read
  only root filesystem, no capabilities, healthcheck), an annotated environment
  template covering every variable the binary reads, and an install guide
  including the EasyPanel API path. `backend/Dockerfile` builds a static binary
  into a `scratch`, non-root image; `frontend/Dockerfile` serves the SPA with
  nginx.
- **CI** — backend (`gofmt`, `go vet`, `go build`, `go test -race -shuffle=on`,
  `go mod verify`, dependency allowlist, `staticcheck`, `govulncheck`, image
  build), a serial MySQL 8 **and** MySQL 9 storage job that fails on any test
  skip, and frontend (`npm ci`, typecheck, Vitest, build, image build).

### Fixed

- Evolution GO webhook subscription: the admin endpoint sent
  `subscribe: ["MESSAGES_UPSERT"]`, an Evolution API v2/Node event name that
  Evolution GO 0.7.2 silently discards — the instance would have been left
  subscribed to nothing. It now sends the 16-name GO enum
  (`MESSAGE, SEND_MESSAGE, READ_RECEIPT, PRESENCE, HISTORY_SYNC, CHAT_PRESENCE,
  CALL, CONNECTION, LABEL, CONTACT, GROUP, NEWSLETTER, QRCODE, BUTTON_CLICK,
  PICTURE, USER_ABOUT`).
- Evolution ingress now filters event names before the claim: non-message events
  (`Receipt`, `Presence`, `Label`, `Connection`, …) are acknowledged and
  ignored instead of being written into Chatwoot as `[unknown]` messages.
- The resolved Chatwoot conversation id is persisted on `contacts_map`, so a
  conversation started from WhatsApp no longer drops the agent's reply with
  `no_mapping`.
- Admin secrets are write-only: they are never returned by the API, readback is
  masked, and replacement is the only mutation. The only token-bearing
  responses are the two per-connection webhook-URL endpoints
  (`Cache-Control: no-store`).
- Deleting a connection deactivates and preserves history instead of deleting
  rows.
- Credentials are redacted from configuration error messages and the MySQL DSN
  is never logged.

### Security

- Webhook ingress is token-authenticated and fails closed: the per-connection
  path token is derived (`base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET,
  "webhook:" + connectionID))`), never stored, and a wrong or missing token is
  `401` — never "delivered but ignored".
- Production posture is the default: `WEBHOOK_TOKEN_SECRET` and `SESSION_SECRET`
  are mandatory unless `APP_ENV=development`, and configuration fails fast when
  one is missing.
- Optional Chatwoot webhook HMAC verification (`CHATWOOT_WEBHOOK_SECRET`) —
  when set it is enforced, and a configured signature mode is never silently
  downgraded.
- Admin session cookies are `HttpOnly`, `Secure` and `SameSite=Lax` by default,
  signed with `SESSION_SECRET` and carrying a per-process nonce, so a restart
  invalidates previously issued sessions.
- Deployed and verified live on 2026-10-13: both directions, the loop-break on
  both sides, webhook fail-closed probes and identity reuse without creating an
  entity.

[Unreleased]: https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/releases/tag/v1.0.0
