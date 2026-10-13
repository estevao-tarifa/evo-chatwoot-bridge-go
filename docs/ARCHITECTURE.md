# Architecture — evo-chatwoot-bridge (Go rewrite)

Decisions trace to ADRs in `docs/adr/`. ADRs 0001–0009 are **Accepted** (see
`docs/adr/README.md`); any remaining `Proposed` label in an individual ADR
header is stale. Facts that could not be verified without live instances/DB are
marked `[needs live validation]` (`AGENTS.md` §9) and must be re-checked against
live instances/DB.

## 1. System overview

```
 Evolution GO ──webhooks──▶┌─────────────────────────┐──webhooks──▶ Chatwoot
      ▲                    │   backend (Go binary)   │                  ▲
      └────send/media──────│  httpapi → bridge core  │────send/media────┘
                           │  dedup · worker · auth  │
                           └───────────┬─────────────┘
                                       │ database/sql
                                  MySQL 8 (legacy schema, ADR-0002)

 React SPA ──REST/JSON (ADR-0001, CORS allowlist)──▶ backend
```

- **backend/** — one static Go binary: HTTP API, webhook ingress, message
  engine, clients, worker pool, auth, observability (ADR-0007: never serves the
  SPA).
- **frontend/** — React 19 SPA consumed via REST only; deployable independently;
  never a runtime dependency of the backend.
- No broker, no microservices, no ORM. Module path:
  `github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend`, `go 1.27`.

## 2. Backend module map

| Package | Responsibility (single) | May import (internal) | Must NOT import |
|---|---|---|---|
| `cmd/bridge` | composition root: flags, config load, manual DI wiring, signal handling, startup/shutdown order | everything (only package that may) | — |
| `internal/config` | env parsing + validation, fail-fast; typed `Config` struct | *(stdlib only)* | all others |
| `internal/observability` | `slog` JSON logger, **payload sanitizer**, prometheus registry, correlation/trace IDs, metrics helpers | `config` | domain, transport, SQL, clients |
| `internal/webhookauth` | pure webhook signature verification (HMAC-SHA256 over `timestamp.rawBody`, `sha256=` prefix, constant-time); stdlib only | *(stdlib only)* | all others |
| `internal/worker` | domain-agnostic bounded queue: capacity, worker pool, retry/backoff, drain, queue metrics (ADR-0005) | `observability` | domain, transport, SQL, clients |
| `internal/dedup` | idempotency state machine + local cache; declares its `Store` port; owns `ProcessedMessage` (ADR-0004) | `config`, `observability` | transport, SQL, clients, `bridge` |
| `internal/bridge` | **domain core**: event parsing (dual PascalCase/camelCase), direction rules, JID/group identity resolution, conversation routing policy, mentions LID→phone, media orchestration, call handling, loop detection; declares ports (`Evolution`, `Chatwoot`, repos) | `config`, `observability`, `dedup`, `worker` | `evolution`, `chatwoot`, `storage/mysql`, `httpapi` |
| `internal/evolution` | Evolution GO HTTP client (net/http): send text/media (caller-provided correlation id), instance/connect, status; optional mark-read only with verified GO path+body, media download disabled pending a proven contract (ADR-0008); implements `bridge.Evolution` port | `config`, `observability`, `bridge` (types/ports only) | `storage/mysql`, `httpapi`, `worker`, `chatwoot` |
| `internal/chatwoot` | Chatwoot HTTP client: contacts, conversations, messages (JSON+multipart), inboxes/settings, agents, health; implements `bridge.Chatwoot` port | `config`, `observability`, `webhookauth`, `bridge` (types/ports only) | `storage/mysql`, `httpapi`, `worker`, `evolution` |
| `internal/storage/mysql` | all SQL: repositories for the 8 legacy tables + `processed_messages`; migration runner + embedded `migrations/*.sql` (ADR-0002); implements `dedup.Store` and bridge/auth/notifications repo ports | `config`, `observability`, domain packages for types (`bridge`, `dedup`, `auth`, `notifications`) | `httpapi`, `evolution`, `chatwoot`, `worker` |
| `internal/auth` | admin login/session (HMAC cookie), bcrypt, login rate limit, `ADMIN_USER` bootstrap, `token_version` revocation (ADR-0006); declares admin-store port | `config`, `observability` | transport, SQL, clients, `bridge` |
| `internal/notifications` | failure counters, threshold/cooldown, alert channels (WhatsApp via sender port, SMTP), alert history (legacy parity) | `config`, `observability` | transport, SQL, clients |
| `internal/httpapi` | REST transport only: ServeMux routes, middleware (auth, CORS, body limits, logging, recovery), OpenAPI coverage test; webhook handlers = token check + claim + enqueue | `config`, `observability`, `bridge`, `auth`, `webhookauth` | `evolution`, `chatwoot`, `storage/mysql` |

Support dirs: `backend/migrations/` (SQL, embedded), `backend/api/openapi.yaml`
(contract, ADR-0001), `backend/tests/` + `testdata/` (fixtures, integration,
load).

### Dependency direction rules

1. **`cmd/bridge` is the only wiring point.** Concrete implementations
   (`evolution`, `chatwoot`, `storage/mysql`) are constructed there and passed
   to consumers as the small interfaces declared **consumer-side** (Go idiom;
   enables fake-based regression tests without a mocking framework).
2. **Domain packages never import transport or SQL.** `bridge` and `dedup` must
   not know about `net/http`, `database/sql`, or the adapter packages. They
   describe what they need as ports; adapters implement them.
3. **Adapters may depend on the domain, never the reverse.** `evolution`,
   `chatwoot`, `storage/mysql`, `httpapi` import domain types/ports; the domain
   graph is acyclic: `bridge → dedup → {config, observability}`.
4. **`worker` stays domain-agnostic.** It moves `func(ctx) error` jobs; payload
   types live in the domain that enqueues them. (Upgrade path if instances ever
   need shared work: swap the enqueue seam for a DB-backed queue — ADR-0005.)
5. **Leaves stay leaves.** `config`, `observability`, `worker` never import
   other internal packages beyond what the table allows.
6. **Enforcement in CI**: `golangci-lint` `depguard` rules encode rules 2–5;
   a violation fails the backend pipeline. OpenAPI coverage test guards the
   transport ↔ contract boundary.

## 3. Key flows

**Inbound webhook (ACK path — no upstream API calls):**
1. `httpapi` middleware: body cap (`http.MaxBytesReader`), recover, sanitized log.
2. Handler validates `{connectionId}/{token}`; unknown/inactive → 404/403.
3. Envelope parsed minimally (event type + stable message ID).
4. `dedup.Claim` → INSERT (ADR-0004). Duplicate → 200 immediately.
   Delivery-eligible events require a stable ID; never bypass the durable claim.
   Missing IDs reject delivery without entity creation (engine classification gate).
5. Persist sanitized allowlisted audit metadata; write failure → non-2xx.
6. Non-blocking enqueue (`worker`). Buffer full → guarded pre-admission release
   → 503. Never release after admission on request cancellation.
7. **200 to the sender.** Accepted/observed, not proof of delivery or lossless
   replay. Target: p95 ≤ 50 ms (§7).

**Async processing:** worker → `bridge.Engine.HandleEvent` → parse full payload
→ resolve identity (JID-first) → reuse mapping before any creation → Chatwoot
forward → mark `sent`/`failed`/`duplicate` with `claimed_by` guard → write
`message_logs` (sanitized).

**Outbound (Chatwoot → WhatsApp):** webhook → claim → worker → map lookup
(connection-scoped) → Evolution send → store `destination_message_id` → echo
from Evolution matches destination index → `duplicate`, never re-sent (loop
protection, AGENTS.md §7 rule 6).

## 4. Identity integrity invariants (AGENTS.md §7)

| Rule | Enforcement point |
|---|---|
| External message never creates a new contact/conversation when identity exists | single `bridge.IdentityResolver` entered by *every* inbound path; creation only via explicit routing-policy decision |
| Reuse before create | resolver API returns mapping; creation is a separate, policy-gated call — structural, not disciplined |
| Group identity = stable JID (connection + account scoped) | keyed by JID, never display name; `contacts_map.whatsappPhone` stores the JID for groups |
| Name vs group name never blindly overwritten | name updates only when payload carries a valid, different name; never `"Grupo"`, never raw phone/JID (legacy H3 regression test) |
| Conversation title follows domain rules, never a JID | title built by routing policy function only |
| Bridge-originated messages never re-sent | `processed_messages.destination_message_id` lookup + terminal `duplicate` state (ADR-0004 §7) |
| Conversation creation only per routing policy | creation is one choke point in `bridge`, covered by decision-matrix tests |
| Reuse respects connection/inbox/state | all map queries are `connection_id`-scoped; inbox comparison is exact, no cross-connection search (legacy H7) |

DB backstops: proposed non-unique index on
`contacts_map(connectionId, whatsappPhone)` now; `UNIQUE` only after H2 is
proven and duplicates cleaned with operator approval (ADR-0002 §4).

## 5. Data ownership

One writer per table: `storage/mysql` is the only package issuing SQL; domain
packages hold ports, not connections.

- Proposed incremental indexes (migration `0004_indexes`): `contacts_map`
  `(connectionId, whatsappPhone)`; `message_logs` `(connectionId, createdAt)`
  for the logs API and `(status, createdAt)` for dashboard counts;
  `processed_messages` indexes per ADR-0004.
- No legacy column is modified or dropped; `message_logs` gains only additive
  columns; `__drizzle_migrations` untouched (ADR-0002).

## 6. Lifecycle

Startup: `config` (fail-fast) → MySQL pool + migrations (advisory lock) →
repos/clients/engine wiring → `ADMIN_USER` bootstrap → webhook bootstrap
(Chatwoot only, legacy parity — Evolution webhook stays manual) → stale-claim
recovery scan → HTTP server → workers.

Shutdown (SIGTERM): stop accepting → drain queue ≤ 30 s (ADR-0005) → cancel
contexts → close DB. Unfinished claims remain `processing`; next boot reconciles
unknown outcomes without replay (ADR-0004 Amendment 3). Audit data cannot restore
stripped content/media. Safe recovery of provably unstarted jobs remains a
separate engine no-loss design/test gate. Logs to stdout are sanitized JSON.

## 7. Performance criteria

Reference host: 4 vCPU / 4 GiB container, MySQL 8 same host, stub upstreams
(`httptest`), 1 KiB synthetic payloads, synthetic fixtures only. Each criterion
is one reproducible command recorded in the report; numbers are re-validated on
the deploy target.

| # | Criterion | Target | Measurement |
|---|---|---|---|
| P1 | Webhook ACK latency (includes claim and sanitized audit write) | **p95 ≤ 50 ms, p99 ≤ 150 ms** at 100 rps sustained / 200 concurrent | stdlib Go load generator (`backend/tests/load`), HDR-style histograms |
| P2 | Sustained end-to-end processing | **≥ 100 msg/s** with stub upstreams; queue depth < 50% of capacity at that rate | load generator + `bridge_queue_depth` |
| P3 | Comparative vs legacy Node | **≥ 2× throughput and ≤ 50% ACK p95** on the identical scenario (legacy run via docker, same stubs) | report table |
| P4 | Backpressure behaviour | Overflow → 503 + `bridge_queue_rejected_total` increments; **process stays alive, no OOM** under 10× burst | burst test; RSS monitored |
| P5 | Memory ceiling | RSS **≤ 256 MiB** at P2 load; post-warmup 30-min soak growth **≤ 5%**; goroutine count stable (`goleak` in tests) | `runtime/metrics` + pprof |
| P6 | DB pool — sizing & safety | per instance `MaxOpenConns=10, MaxIdle=5, ConnMaxLifetime=5m, ConnMaxIdleTime=60s`; bridge total ≤ **50%** of MySQL `max_connections`; claim INSERT adds ≤ 5 ms p95 to ACK; every query has a context deadline (5 s; 30 s for media ops) | config defaults + `db.Stats()` snapshot in tests |
| P7 | Payload caps | webhook body default **16 MiB** (configurable; legacy allowed 50 MB — real ceiling `[needs live validation]`, Q7); media **≤ 16 MiB decoded** (base64 ≈ 22 MiB); oversize → reject with metric, never partial processing | boundary tests at cap−1 / cap / cap+1 |
| P8 | Graceful shutdown | drain ≤ 30 s; **zero double-sends** after SIGTERM; unresolved jobs visible within 60 s of restart, not falsely counted as delivered | kill-test harness; safe unstarted-work recovery remains an engine gate |
| P9 | Restart reconciliation | stale claims quarantined as `failed/unconfirmed_send` without external replay; bounded scans must cover backlog; no silent drops | seeded DB fixtures + boot; quarantine alone does not pass the engine no-loss gate |

Method notes: parsers/mappers/token-bucket get `go test -bench` (ns/op, allocs);
concurrency invariants get N-way race tests (`-race`); throughput numbers are
meaningless without the exact command — report must include it. Tunables if a
target misses: `workers`, `queue_capacity`, DB pool — never unbounded growth.

## 8. Security invariants (summary)

Admin `/api/v1/*` routes require a session except documented login/setup/meta
and probes; no public-admin fallback exists (ADR-0006). The token-authenticated
`/api/webhooks/*` routes are exempt from admin session middleware, not from auth.
Webhooks: per-connection URL token (mandatory, independent of admin session).
The path token is masked in every log/error/metric path (`httpapi.sanitizePath`,
byte-length preserving so a non-ASCII path cannot shift the mask; everything
after the system segment is masked, including the connection-id slot)
and the transport rejects unclean webhook paths (`//`, `/./`, `/../`) with 404
before the `ServeMux` can redirect the raw token into a `Location` header; the
tokenless `/{connectionID}` form is registered explicitly and fails closed.
`WEBHOOK_TOKEN_SECRET` is required unless `APP_ENV` is explicitly `development`
(production is the default posture).
Upstream secret verification: pinned Chatwoot v4.12.1 API-inbox delivery is
unsigned; account-webhook HMAC is a separate unused mode (ADR-0009). Evolution
secret/HMAC capability remains `[needs live validation]` — nothing is promised
or enabled before a specialist verifies it. Never silently downgrade a
configured signed mode. Secrets (tokens, cookies, headers, base64, payloads)
never logged; the `observability` sanitizer is the single choke point (ADR-0006 §9).

## 9. Open questions / unverified facts

| # | Question | Status | Resolved by |
|---|---|---|---|
| Q1 | Evolution GO `/send/text` + `/send/media` response shape (ID extraction — legacy H4) | `[needs live validation]` | legacy fixtures; real payloads |
| Q2 | Evolution GO webhook auth capabilities (secret/HMAC?) | `[needs live validation]` | official docs; live instances |
| Q3 | Chatwoot webhook signature/secret support | `[needs live validation]` | official docs; live instances |
| Q4 | Upstream retry behaviour on 503 (ADR-0005 backpressure) | `[needs live validation]` | live instances |
| Q5 | Evolution GO/Chatwoot re-delivery horizon (dedup TTL sizing) | `[needs live validation]` | live instances; defaults generous meanwhile |
| Q6 | Production DB state: `__drizzle_migrations` variant, `bridge_settings` presence, `contacts_map` duplicates (H2) | `[needs live validation]` | homologation; fixture assumptions documented |
| Q7 | Media size limits actually enforced by Evolution GO; whether media (base64) arrives inline in webhook payloads (drives P7 body cap) | `[needs live validation]` | live instances; our caps are ours to enforce meanwhile |

## 10. Risks (severity)

| Risk | Sev | Mitigation |
|---|---|---|
| Double send during transition if legacy + Go bridge run the same connection concurrently | **CRITICAL** | operational rule: one bridge per connection; dedup is per-system so never claim compatibility (ADR-0002 consequences) |
| Silent event loss if upstream does not retry after 503/queue-full | **HIGH** | oversized default buffers + rejection metric + alert; Q4 verification (ADR-0005) |
| Identity duplicates persist if H1/H2 fixes incomplete (contact search formatting, races) | **HIGH** | resolver invariants §4, regression tests fail-then-pass; DB UNIQUE deferred until data proven clean |
| `SESSION_SECRET`/webhook token mishandling (weak secret, URL leaked in logs) | **HIGH** | masking + rotation + fail-fast on missing secret (ADR-0006) |
| Migration runner DDL privileges at boot | **MEDIUM** | advisory lock, static embedded SQL, `-skip-migrate` (ADR-0002) |
| ACK path depends on MySQL availability | **MEDIUM** | claim failure → 503 (upstream retry); pool tuning P6; health endpoints |
| Cross-origin cookie misconfig locks admin out | **MEDIUM** | same-site reverse-proxy topology recommended (ADR-0007); compose ships both variants |
| Perf targets missed on deploy target | **LOW** | P-targets re-validated on the deploy target; tunables bounded |
