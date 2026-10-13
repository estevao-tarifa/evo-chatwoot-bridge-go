# Security policy

## Supported versions

The current release line is **1.0** (`1.0.x`). The latest release and the tip of `main` are supported;
there are no maintained release branches yet. Fixes land on `main` and the
[CHANGELOG](CHANGELOG.md) records them.

## Reporting a vulnerability

**Do not open a public issue, discussion or pull request for a security
problem.** A public report exposes every other installation before a fix
exists.

Report privately through **GitHub Security Advisories**:

1. Open <https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/security/advisories/new>
   (repository → *Security* → *Advisories* → *Report a vulnerability*).
2. Describe the issue with enough detail to reproduce it.
3. If you already have a fix, attach it as a patch in the advisory draft — not
   as a commit on a fork that is already pushed.

Please include:

- affected version or commit (`git rev-parse HEAD`, or the image tag);
- the deployment shape (Docker Compose or EasyPanel, MySQL version, whether a
  reverse proxy terminates TLS);
- the impacted component: webhook ingress, admin REST API, SPA, migration
  runner, or an upstream client (`evolution/`, `chatwoot/`);
- a minimal reproduction and the observed vs. expected result;
- an impact assessment (what an attacker gains, and what they must already
  control);
- whether the issue is already public somewhere.

### What to expect

This is a small project maintained on a best-effort basis, so no SLA is
promised. Reports are acknowledged, triaged by severity (CRITICAL / HIGH /
MEDIUM / LOW) and answered with a remediation plan or a reasoned decision not to
act. Credit is given in the advisory and the CHANGELOG unless you ask to stay
anonymous. Please allow a fix to ship before disclosing publicly.

## Never paste production payloads or tokens

Treat every value that crosses this bridge as production secret material:

- **Webhook payloads carry credentials.** An Evolution GO webhook body contains
  the instance's `instanceToken` alongside message content. Chatwoot payloads
  contain message bodies, contact names and phone numbers. A raw payload pasted
  into an issue is a credential leak *and* a personal-data leak.
- **Never paste**: webhook payloads, `apikey` / `api_access_token` headers,
  signed webhook URLs (they embed a per-connection token), session cookies,
  `DATABASE_URL` (it contains the MySQL password), `.env` files, `docker inspect`
  output, screenshots of the admin UI or of the Evolution/Chatwoot panels, real
  phone numbers, contact names or message content.
- Redact before sharing: replace tokens with `<redacted>`, hosts with
  `bridge.example.com`, numbers with `15550000000`, and payload bodies with
  `{ ... }`. Reproduce with **synthetic** fixtures — the test suite in
  `docs/TESTING.md` only ever uses synthetic data.
- Masked values are already available for the admin surface: `GET
  /api/v1/connections/{id}/webhook-url/*` returns the token-bearing URL only to
  an authenticated session, and the token is masked everywhere it is logged.

## Secrets to rotate after any exposure

If any of these leaked (in a log, a paste, a screenshot, a commit, a shared
host, or the advisory itself), rotate it **before** anything else. Rotation
order matters: the derived webhook URLs become invalid the moment
`WEBHOOK_TOKEN_SECRET` changes.

| Secret | Where it lives | Rotation effect |
|---|---|---|
| `WEBHOOK_TOKEN_SECRET` | bridge env | Derives every per-connection webhook path token (`base64url(HMAC-SHA256(secret, "webhook:"+connectionID))`). Rotating invalidates **both** webhook URLs — re-wire Evolution and Chatwoot afterwards (see `deploy/README.md`). |
| `SESSION_SECRET` | bridge env | Signs the admin session cookie. Rotating logs every admin out; sessions also die on restart by design (per-process nonce). |
| `ADMIN_PASS` | bridge env (bootstrap) and the `admin_users` row | Change it in the admin UI / `admin_users`; any bootstrap value in the environment is only read when the table is empty. |
| MySQL password | `DATABASE_URL` | Rotate in MySQL *and* in the bridge env, then redeploy. The old password grants full access to the shared schema. |
| Evolution **instance token** | `apikey` on instance-scoped calls; stored per connection | Rotate in the Evolution panel and update the connection in the admin UI/API. |
| Evolution **global API key** | `GLOBAL` API key, used to resolve/list instances | Rotate in the Evolution panel; it can reach every instance on that server. |
| Chatwoot **API access token** | `api_access_token` on every Chatwoot call | Rotate in the Chatwoot profile; update the connection. It can read and send messages in the account it belongs to. |
| `CHATWOOT_WEBHOOK_SECRET` (optional) | bridge env | Only set when account-webhook HMAC mode is used; rotating requires re-registering the Chatwoot webhook with the same secret. |

A leaked `.env` also exposed every value in it at once — rotate all of the above,
not just the one you noticed.

## Hardening notes for operators

- Keep MySQL off the public internet. The bridge is the only component that
  should reach it, over a private Docker network.
- Terminate TLS at your reverse proxy and keep `COOKIE_SECURE=true` (the
  production default). The session cookie is not sent over plain HTTP.
- Keep `APP_ENV=production`: it is what makes `WEBHOOK_TOKEN_SECRET` and
  `SESSION_SECRET` mandatory and makes the process fail fast when one is missing.
- The webhook routes are token-authenticated and fail closed; an unconfigured or
  wrong token answers `401`, never "delivered but ignored".
- Run exactly one replica. Sessions carry a per-process nonce and the queue is
  in-process; the deployment artifacts pin `replicas: 1`.

## Out of scope

Reports about the upstream projects themselves (Evolution GO, Chatwoot, MySQL,
Traefik) belong to their own trackers. Misconfiguration of a public
deployment — MySQL exposed to the internet, `APP_ENV=development` in
production, secrets committed to a private repo — is an operator issue, but
please still tell us if the documentation made it easy to get wrong.
