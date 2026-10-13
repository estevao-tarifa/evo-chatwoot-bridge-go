# ADR-0006 — Mandatory admin authentication; restricted webhook surface

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-0001 (CORS), ADR-0002 (`connections.webhook_token` migration)

## Context

Two verified legacy holes must not be reproduced (`AGENTS.md` §8, plan §11):

- `bridgeProcedure` degrades to `publicProcedure` when OAuth env vars are
  absent — i.e. **all admin routes become unauthenticated** in the standalone
  mode this rewrite replaces.
- Webhook URLs are `POST /api/webhooks/{evolution|chatwoot}/:connectionId`
  with a **guessable numeric ID and no authentication of any kind**.

The Go rewrite has no OAuth: standalone admin login is the only mode. The
admin surface must be authenticated, and the webhook surface must be as
restricted as the upstreams actually allow — **without inventing mechanisms
that Evolution GO / Chatwoot may not support**.

## Decision

### Admin authentication (mandatory, no fallback)

1. **Every** `/api/*` route except `POST /api/auth/login`, `POST /api/auth/setup`
   and the read-only liveness probes requires a valid session. Middleware
   returns `401` otherwise. No configuration flag can disable this.
2. Password storage: `bcrypt` (`x/crypto/bcrypt`), cost **12** (≥10 required by
   plan §11; legacy used 10). Bootstrap from `ADMIN_USER`/`ADMIN_PASS` env at
   startup (legacy parity) or first-run `setup`, which is only accepted while
   `admin_users` is empty (legacy parity).
3. **Sessions** — stateless HMAC cookie, stdlib only: token =
   `base64(admin_id | exp | token_version) + HMAC-SHA256(SESSION_SECRET)`.
   - `SESSION_SECRET` required in production (fail-fast if missing); no secret
     is ever logged.
   - Cookie: `HttpOnly`, `Secure` (off only when explicitly configured for
     local HTTP dev), `SameSite` configurable (`Lax` for same-site deployments,
     `None` for split frontend/backend per ADR-0007), max-age **7 days**
     (legacy embedded a 1-year JWT — that lifetime is cut deliberately).
   - Revocation: `token_version` is checked against the `admin_users` row on
     each request (one indexed read). Logout/password change bumps it. This
     keeps statelessness without giving up revocation.
4. **Login rate limiting**: in-memory limiter (per instance — documented
   limitation at >1 instance): 5 failed attempts per (IP, username) per 15
   minutes → `429 + Retry-After`; success resets; constant-time bcrypt
   comparison always (no user-enumeration signal: same response for unknown
   user and wrong password).
5. CSRF: relies on non-simple requests (JSON + `X-CSRF: 1`) + strict CORS
   allowlist (ADR-0001 §6); SameSite adds a second layer.

### Webhook surface (no invented upstream mechanisms)

6. Routes become
   `POST /api/webhooks/evolution/{connectionId}/{token}` and
   `POST /api/webhooks/chatwoot/{connectionId}/{token}` where `token` is a
   per-connection 32-byte `crypto/rand` value stored in the new additive column
   `connections.webhook_token` (ADR-0002 migration `0003`). The URL is treated
   as a bearer secret: never logged in full, masked in every log/error path,
   rotatable from the admin UI. This is entirely under our control — both
   upstreams accept arbitrary webhook URLs — so it is implementable today.
7. **Secret/HMAC validation per upstream: PENDING.** Whether Evolution GO or
   Chatwoot can send a shared secret/signature header is **unverified**
   `[needs live validation]` — no promise is made now. The handler accepts an
   optional verifier (`X-Webhook-Secret`, `hmac.Equal`, off by default) that is
   enabled only after the upstream contract work confirms what each side
   actually sends, with a test. **Do not document or rely on any upstream
   secret mechanism until proven.** Direct consequence: until then, the URL
   token is the only webhook authentication.
8. Oversized/unknown payloads: per-route `http.MaxBytesReader` — webhook body
   cap default **16 MiB** (configurable; the legacy Express app allowed 50 MB
   globally and Evolution may include media/base64 inline — the real ceiling is
   `[needs live validation]`, so the default errs generous and exists to bound
   memory, not to be tight), login/admin JSON cap **256 KiB**. Requests over the
   cap are rejected before parsing (413 + metric).
9. **Logging**: tokens, auth headers, cookies, message bodies, media and full
   payloads are never logged (`AGENTS.md` §8). A central sanitizer in
   `internal/observability` is the only component allowed to shape payload
   logs; `raw_payload` persisted to `message_logs` passes through it (legacy
   already sanitized — keep that property, prove it with tests).
10. **Secrets in admin reads: parity kept.** `connections` list/get return the
    EVO/Chatwoot credentials to the authenticated admin because the SPA edit
    form repopulates from them (legacy contract, from the endpoint audit).
    Consequence: the admin origin is a credential-bearing surface — HttpOnly
    cookie sessions, strict CSP, no third-party scripts. Credentials still never
    appear in logs or error payloads.

## Consequences

- (+) The two verified legacy holes are structurally impossible: no
  public-admin fallback path exists in the router; webhook IDs are no longer
  guessable.
- (−) Webhook URLs must be re-registered in Evolution GO (manual, by design —
  the legacy also never touched EVO’s webhook config on startup) and Chatwoot
  (automatic via `webhookBootstrap` parity). Ops note for the rollout. The
  `webhookStatus.check` equivalent must match the new URL pattern including the
  token segment (implementation note for the admin surface).
- (−) Cookie/`SameSite=None` cross-site deployment needs HTTPS everywhere —
  documented in `docs/ARCHITECTURE.md`.
- (−) In-memory rate limiter is per instance; fine at 1–2 instances, called out
  for honesty.
- Risk: **CRITICAL if webhook URLs leak** (they are credentials) — mitigated by
  masking in logs, rotation endpoint, and (later, if supported) upstream
  secrets. HIGH if `SESSION_SECRET` is weak — deployment doc mandates random
  ≥32 bytes.

## Alternatives

- **Bearer tokens in localStorage** — XSS-exposed; cookie + HttpOnly is
  strictly better for an admin SPA. Rejected.
- **Full JWT library** — unnecessary: HMAC cookie is ~60 lines stdlib
  (ADR-0003); JWT adds a dependency and none of its features are used.
- **IP allowlist as the only webhook control** — insufficient (mobile/dynamic
  senders) and not always operable; kept as an optional config, not the design.
- **Opaque DB-backed sessions table** — extra table and write per request;
  token_version covers revocation. Rejected (YAGNI).

## Amendment (2026-10-10)

`GET /api/v1/meta` is added to the public, unauthenticated surface alongside the
read-only probes (`/healthz`, `/readyz`). It exposes only service name, version
and commit — no configuration, no secrets. Every other `/api/v1/*` route
 requires authentication.

## Amendment 2 (2026-10-12) — derived webhook token (no schema change)

§6 above cites "migration `0003`" for `connections.webhook_token`. That column
does **not** exist: migration 0003 is `contacts_map_index`, and the column is in
neither the legacy schema nor the baseline. Adding it would require a migration,
which is not authorized here (unvalidated against production).

**Decision:** the per-connection webhook token is **derived**, not stored:
`token = base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:" + connectionID))`.
- `WEBHOOK_TOKEN_SECRET` is a required production secret (fail-fast if absent),
  never logged; the derived token is constant-time compared and never logged.
- The token is stable per connection and secret, so no storage/rotation column is
  needed. Rotation = rotate the secret (rotates all connections) — acceptable at
  1–2 connections; a per-connection rotation column can be added later via an
  approved migration when the admin UI needs it.
- A stored-token variant (later migration or `bridge_settings` KV) remains an
  option for a future slice; it is not required now.

**Mandatory leak fix:** the existing HTTP access log records `r.URL.Path`, which
contains the token segment. Ingress must mask the token segment in every log/
error/metric path (path-masking helper), or the token leaks. This is a
CRITICAL-class requirement for the ingress slice.

Consequence: the webhook URL pattern and masking are fixed now; the ADR's
"stored column + rotation endpoint" is superseded by the derived scheme until a
rotation migration is approved.

## Amendment 3 (2026-10-11, pinned Chatwoot v4.12.1)

`/api/webhooks/{evolution|chatwoot}/{connectionId}/{token}` does **not** require
an admin session; the path token is mandatory independent authentication.
The generic optional `X-Webhook-Secret` verifier in the original decision is
not a supported upstream mechanism and must not be enabled without a separate
verified contract. Official v4.12.1 **API-inbox** outbound webhooks do not sign;
the account-webhook HMAC is a different, currently unused delivery mode. Never
treat channel `hmac_token` or delivery ID as outbound authentication. An
operator-configured signed mode must fail closed (no silent fallback to token-only
upon missing/invalid signature). See ADR-0009 and pinned Chatwoot research.

## Amendment 4 (2026-10-12) — secrets are write-only

**Supersedes §10** ("Secrets in admin reads: parity kept").

`connections` list/get and every other admin read **never return** the EVO/Chatwoot
credentials. Secret fields are **write-only**: an update replaces a value; a read
returns a masked marker (e.g. `••••1234`) or an unset indicator. Clearing a secret
is not supported (replace only). Rationale: the mission forbids returning/logging
tokens, and the SPA edit form no longer needs the raw value to function ("set /
replace" UX).

**Narrow exception (R2).** Exactly two endpoints —
`GET /api/v1/connections/{id}/webhook-url/evolution` and
`.../webhook-url/chatwoot` — return the full webhook URL, which embeds the derived
per-connection token, because the operator must register it manually and the
bridge refuses unverified auto-registration. These responses are authenticated,
sent with `Cache-Control: no-store`, never logged, and the token appears in no
other response. Credentials still never appear in logs or error payloads.

## Amendment 5 (2026-10-12) — session token carries a process nonce

**Supersedes §3's** "`token_version` checked against the `admin_users` row on each
request".

The session token is `base64url(admin_id | exp | version | instance_nonce)` +
`.` + `HMAC-SHA256(SESSION_SECRET)`.

- `instance_nonce` is random **per process** and required on validation, so a
  restart rejects every previously issued token. This closes the resurrection hole
  where a logged-out (but unexpired) token became valid again after a restart.
- `version` is an **in-memory, per-instance** counter (no `token_version` column
  exists and no migration ships with it). `Logout` and the exported
  `RevokeSessions` password-change hook bump it; revocation is therefore effective
  in-process only.
- Consequence: **multi-replica deployments require sticky sessions**; every
  restart logs all admins out. Both are accepted until an approved migration
  persists a version column.
