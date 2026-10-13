# Evolution GO ⇄ Chatwoot bridge (Go)

[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](backend/go.mod)
[![backend CI](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/backend.yml/badge.svg)](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/backend.yml)
[![frontend CI](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/frontend.yml/badge.svg)](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/frontend.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Deploy](https://img.shields.io/badge/Deploy-Docker%20%7C%20EasyPanel-2496ED?logo=docker&logoColor=white)](deploy/README.md)
[![Backend: no assets](https://img.shields.io/badge/Backend-serves%20no%20assets-555.svg)](docs/adr/0007-split-backend-frontend-deployments.md)
[![Idempotent](https://img.shields.io/badge/Delivery-at--least--once%20deduped-2ea44f.svg)](docs/ARCHITECTURE.md)

A self-hosted, bidirectional WhatsApp bridge between **[Evolution GO](https://github.com/EvolutionAPI/evolution-go)**
and **[Chatwoot](https://www.chatwoot.com/)**: messages sent by a customer on
WhatsApp land in the right Chatwoot conversation, and every reply an agent types
in Chatwoot goes out on WhatsApp exactly once — even across restarts, retries and
duplicate webhooks.

Written in **Go** (single static binary, no Node/Vite/React at runtime on the
server) with a **React admin SPA deployed independently**, on top of the existing
MySQL schema. Ships with **Docker Compose** and an **EasyPanel** install path.

> Looking for: *whatsapp chatwoot integration*, *evolution api bridge*,
> *chatwoot whatsapp inbox*, *self-hosted whatsapp support desk*, *go webhook
> bridge*, *idempotent whatsapp message forwarding*? This is that.

**Documentation** · [Install](docs/INSTALL.md) · [Configure](docs/CONFIGURATION.md) ·
[Troubleshoot](docs/TROUBLESHOOTING.md) · [Architecture](docs/ARCHITECTURE.md) ·
[REST API](docs/API.md) · [ADRs](docs/adr/) · [All docs](docs/README.md)

---

## Table of contents

- [Why this bridge exists](#why-this-bridge-exists)
- [Features](#features)
- [Architecture](#architecture)
- [Quick start with Docker Compose](#quick-start-with-docker-compose)
- [Install on EasyPanel](#install-on-easypanel)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [Reverse proxy and the optional SPA](#reverse-proxy-and-the-optional-spa)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)
- [Project status and known limitations](#project-status-and-known-limitations)
- [Security](#security)
- [Contributing](#contributing)
- [License](#license)

## Why this bridge exists

Connecting WhatsApp to an omnichannel desk looks simple until messages start
arriving twice, agents reply into the void, or a customer's contact record is
duplicated by a webhook replay. The hard parts are not the HTTP calls; they are
**identity** and **idempotency**:

- A WhatsApp group is the same group tomorrow only if you key it by its **stable
  JID**, never by its display name.
- An externally sent message (WhatsApp Web, another automation, a phone) must
  **reuse** the existing contact and conversation instead of creating a new one.
- A contact name must never be overwritten with `"Grupo"`, a raw phone number or
  a JID.
- A message the bridge itself sent must be recognised when its own echo comes
  back, so it is never forwarded again in a loop.
- Two workers must not both deliver the same webhook.

This bridge is built around those invariants first (see
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and the
[ADRs](docs/adr/)), and it enforces them with a persistent claim table rather
than in-memory caches.

## Features

| | |
|---|---|
| **Bidirectional** | WhatsApp → Chatwoot and Chatwoot → WhatsApp, text plus media captions |
| **Exactly-once delivery semantics** | a persistent claim table (`processed_messages`) with a single-winner protocol; a duplicate webhook can never produce a duplicate message |
| **Safe on unknown outcomes** | a send whose result cannot be confirmed is quarantined and paged in the logs — never blindly retried, never duplicated |
| **Identity integrity** | contact/conversation reuse before creation, groups keyed by stable JID scoped to connection + account, names never overwritten with placeholders |
| **Loop protection** | the bridge recognises the echo of its own sends on both sides of the flow |
| **Conversation mapping** | the resolved Chatwoot conversation id is persisted, so an agent reply on a WhatsApp-first conversation routes back correctly |
| **Per-connection webhook tokens** | `base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:<connectionId>"))`, constant-time compared, fail-closed |
| **Authenticated admin API** | session cookies, CSRF header, bcrypt admin users, masked secrets, deliberate no-enumeration login |
| **Event allow-list** | only real message events are processed; presence/receipt/label/connection noise is acknowledged and discarded before any DB write |
| **Forward-only migrations** | checksum-locked, advisory-locked (`GET_LOCK`), additive; verified against a legacy schema on MySQL 8 and MySQL 9 |
| **Two deploy paths** | plain Docker Compose, or a managed app in EasyPanel (the install guide includes the API-driven path) |
| **Splittable frontend** | the backend serves **no** assets; the SPA is an independent nginx container (ADR-0007) |

## Architecture

```mermaid
flowchart LR
  WA["WhatsApp"] -->|messages| EVO["Evolution GO<br/>(WhatsApp gateway)"]
  EVO -->|"POST /api/webhooks/evolution/{id}/{token}"| GO["bridge (Go)<br/>claim → route → deliver"]
  CW["Chatwoot"] -->|"POST /api/webhooks/chatwoot/{id}/{token}"| GO
  GO -->|Chatwoot REST| CW
  GO -->|Evolution REST| EVO
  GO <--> DB[("MySQL<br/>connections · contacts_map<br/>message_logs · processed_messages")]
  SPA["React admin SPA<br/>(nginx, independent)"] -->|"/api/v1/*"| GO
```

One binary, one MySQL database, no message broker. Inside the binary:

```
internal/
  config        env contract, validation
  httpapi       admin REST + webhook ingress (claim-before-ACK)
  webhookauth   per-connection URL tokens, Chatwoot HMAC
  bridge        routing matrix, resolver, engine, echo/loop break
  evolution     Evolution GO client (send, webhook, instance)
  chatwoot      Chatwoot client (contacts, conversations, messages, inboxes)
  dedup         single-winner claim store
  storage/mysql persistence: connections, mappings, logs, claims, migrations
  worker        bounded in-process queue with graceful drain
  admin, auth   sessions, CSRF, bcrypt users
  observability structured logs, payload sanitiser, correlation ids
```

Full detail: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · module map and
dependency rules are documented there.

## Quick start with Docker Compose

Prerequisites: a Linux host with Docker, an existing **MySQL 8/9** database, a
running **[Evolution GO](https://github.com/EvolutionAPI/evolution-go)** instance
connected to WhatsApp, and a **Chatwoot** account with an API-channel inbox.

```sh
git clone https://github.com/estevao-tarifa/evo-chatwoot-bridge-go.git
cd evo-chatwoot-bridge-go

cp deploy/example.env deploy/.env
# edit deploy/.env: DATABASE_URL, WEBHOOK_TOKEN_SECRET, SESSION_SECRET,
# ADMIN_USER/ADMIN_PASS, BRIDGE_URL (your public URL)

# 1. create/upgrade the schema — ALWAYS before the first start
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate

# 2. run it
docker compose -f deploy/docker-compose.prod.yml up -d

# 3. verify
curl -fsS http://127.0.0.1:8100/healthz && echo " up"
curl -fsS http://127.0.0.1:8100/readyz  && echo " db reachable"
```

Then create the connection (admin API or the SPA) and point the two webhooks at
it — the step-by-step, including the exact `subscribe` list Evolution GO accepts,
is in **[docs/INSTALL.md](docs/INSTALL.md)**.

> Generate secrets with `openssl rand -base64 32`. Never commit `deploy/.env`;
> the `.gitignore` already excludes it.

## Install on EasyPanel

EasyPanel (Traefik + Docker Swarm) is the reference deployment this project has
been validated on, end to end, in production.

The full walkthrough — create the app from this Git repository, set the
environment, run the one-shot migration **before** the server, attach a domain,
route `/api` for the SPA, and flip the two webhooks — is in
**[docs/INSTALL.md](docs/INSTALL.md)**, with the API-driven variant in
[deploy/README.md](deploy/README.md).

```text
EasyPanel project
├── app  bridge          ← this repo, build path /backend, internal port 3000
│    env: DATABASE_URL, BRIDGE_URL, WEBHOOK_TOKEN_SECRET, SESSION_SECRET, ADMIN_*
└── app  bridge-ui       ← optional, build path /frontend, internal port 80
     env: BRIDGE_API_BASE_URL=https://<your-ui-domain>   (same origin ⇒ no CORS)
     domain:<your-ui-domain>/     → bridge-ui
     domain:<your-ui-domain>/api  → bridge
```

## How it works

**Ingress is claim-before-ACK.** A webhook is authenticated by its per-connection
path token, filtered by an event allow-list, then *claimed* by exactly one worker
before it is acknowledged; a second delivery of the same message loses the claim
and is answered `200` without side effects.

**Routing is a frozen matrix.** Payload-only filters run first (event name,
message type, private notes, agent bots), then the identity is resolved: **mapping
first**, then an exact search, and only then a creation — gated on an active
connection, a valid inbox, and an authoritative name for groups. A group whose
only available name is the placeholder `"Grupo"` is never created.

**Delivery is one attempt per claim.** A send with a correlatable message id
completes the claim; a refusal fails it; an unknown outcome quarantines it and
raises a log, keeping rule 6 (never re-send a bridge-originated message) intact
without risking a lost message being replayed blindly.

**Both directions are symmetric.** Chatwoot → WhatsApp resolves the stored
conversation mapping (written when the bridge resolved the conversation) and
sends through Evolution; the echo of that send comes back as a `SendMessage`
event and is recognised, not forwarded.

## Configuration

Everything is environment-driven; the annotated reference is
[deploy/example.env](deploy/example.env) and the behaviour is documented in
[docs/CONFIGURATION.md](docs/CONFIGURATION.md).

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | `mysql://user:pass@host:3306/database` |
| `PORT` | HTTP port (default `3000`) |
| `APP_ENV` | `production` \| `development` |
| `BRIDGE_URL` | the public base URL of this bridge (used to derive webhook URLs) |
| `WEBHOOK_TOKEN_SECRET` | secret behind every per-connection webhook token |
| `SESSION_SECRET` | admin session signing |
| `ADMIN_USER`, `ADMIN_PASS` | bootstrap admin, created only when no admin exists |
| `CHATWOOT_WEBHOOK_SECRET` | optional; when set, the Chatwoot route also requires a valid account HMAC |
| `COOKIE_SECURE`, `COOKIE_SAMESITE` | session cookie policy |
| `CORS_ALLOWED_ORIGINS` | only needed when the SPA is served from another origin |
| `LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error` |

Rotating `WEBHOOK_TOKEN_SECRET` invalidates both derived webhook URLs — re-apply
them on Chatwoot and Evolution afterwards.

## Reverse proxy and the optional SPA

The backend serves **no** frontend assets (ADR-0007): `GET /index.html` on the
bridge is a `404` by design. The admin SPA is a separate nginx container whose
runtime config comes from `BRIDGE_API_BASE_URL`:

- **same-origin (recommended):** serve the SPA and proxy `/api` to the bridge on
  one domain — no CORS, first-party session cookie. The SPA's route templates
  already include `/api/v1`, so `BRIDGE_API_BASE_URL` must be an origin **without**
  a trailing `/api` (a trailing `/api` double-prefixes to `/api/api/v1/…`).
- **cross-origin:** set `BRIDGE_API_BASE_URL` to the bridge origin and add the SPA
  origin to `CORS_ALLOWED_ORIGINS`.

The SPA is optional at runtime: stopping it never affects delivery.

## Troubleshooting

The most common failure modes, with the symptom, the cause and the fix, are
collected in **[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)** — including
webhook `401`s, an Evolution instance that silently stops delivering because its
`subscribe` list was replaced with v2 event names, the migrate-before-start
ordering, and the SPA calling the wrong API origin.

## FAQ

<details>
<summary><strong>Does it need Redis, Kafka or a queue service?</strong></summary>

No. A bounded in-process worker queue plus the MySQL claim table provide the
delivery guarantees; the queue drains on `SIGTERM`.
</details>

<details>
<summary><strong>Can I run more than one replica?</strong></summary>

Today the claim protocol is safe per replica, but the `contacts_map` uniqueness
index and the upsert conflict fallback are still on the roadmap; run a **single
replica** unless you have applied them. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §10.
</details>

<details>
<summary><strong>Does it rewrite the legacy schema?</strong></summary>

No. Migrations are forward-only and additive; existing tables are never altered
destructively, and `__drizzle_migrations` is never touched.
</details>

<details>
<summary><strong>What happens if Chatwoot is down?</strong></summary>

Reads are retried with bounded exponential backoff; a mutation with an unknown
outcome is quarantined (claim closed, no retry) so a message is never delivered
twice. Quarantined claims are visible in the logs and in the claim table.
</details>

<details>
<summary><strong>Are group chats supported?</strong></summary>

Yes. A group is keyed by its full JID scoped to connection and account, its name
comes only from an authoritative `groupData.name`, and groups can be ignored
per connection.
</details>

<details>
<summary><strong>Does the backend serve the admin UI?</strong></summary>

No, deliberately (ADR-0007). The SPA is deployed independently, so a backend
deploy never ships frontend assets and vice versa.
</details>

## Project status and known limitations

The core is production-validated: bidirectional text delivery, the Chatwoot →
WhatsApp leg, the echo/loop break, identity reuse without creating duplicate
entities, and the event allow-list were all exercised with live traffic. Known
gaps, stated honestly:

- **Quarantine over retry (B1).** A send whose outcome is unknown is quarantined
  rather than retried. That trades a rare dropped message for a guaranteed
  no-duplicate delivery. Documented decision, not an accident.
- **Loop-break depends on the created Chatwoot message id (B5).** If Chatwoot
  ever returned a send without a correlatable id, the echo would be classified
  externally; such outcomes are quarantined instead.
- **Single replica** until the `contacts_map(connectionId, whatsappPhone)` unique
  index and the upsert conflict fallback land.
- **Metrics and performance work (planned)** — Prometheus metrics and the
  benchmark suite are specified but not merged.
- **Admin `instanceStatus`** may show `unknown` on Evolution GO 0.7.2, whose
  status payload shape (`{"data":{"Connected":true,…}}`) is not in the mapper.
- Per-connection behaviour toggles beyond `ignoreGroups` (message signing,
  auto-read, call rejection, inbox behaviours) are persisted and editable in the
  API but not yet enforced by the engine.
- The integration suite needs `-timeout 30m` on slow, shared hosts.

The roadmap is tracked in the issue tracker and reflected in
[CHANGELOG.md](CHANGELOG.md): next come the cross-replica uniqueness index,
Prometheus metrics with the benchmark suite, and enforcement of the remaining
per-connection toggles.

## Security

- Webhook routes fail closed: a missing or wrong token is a `401`, never a
  redirect, and an unset `WEBHOOK_TOKEN_SECRET` answers `503`.
- Admin routes require a session; mutations additionally require the `X-CSRF: 1`
  header and a JSON content type. Login never distinguishes unknown users.
- Tokens, cookies, auth headers, media base64 and message bodies are never
  logged; webhook payloads are sanitised before they are persisted (the Evolution
  payload carries `instanceToken` — treat payloads as secret material).
- Report a vulnerability privately: see [SECURITY.md](SECURITY.md).

## Contributing

Workflow, commit conventions, the mandatory test/QA/review gates and the Go style
requirements are in [CONTRIBUTING.md](CONTRIBUTING.md). The Go engineering
principles this codebase is written to are summarised in
[docs/SKILLS.md](docs/SKILLS.md).

## License

[MIT](LICENSE).
