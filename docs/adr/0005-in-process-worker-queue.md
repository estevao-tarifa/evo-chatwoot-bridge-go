# ADR-005 — In-process worker queue with bounded capacity

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-004 (claim rows + recovery), ADR-003 (no broker dependency)

## Context

Webhook handlers must ACK in milliseconds (Evolution/Chatwoot have their own
timeouts), while processing an event involves DB reads, two external APIs and
media — seconds. The legacy solved this with `res.status(200)` then a floating
`await` (no limits: unbounded concurrency, lost work on restart, no retry
discipline). The rewrite must keep the ACK-fast property while bounding memory
and giving explicit retry/backpressure semantics. Deployment model: **one
static binary + MySQL** — an external broker (Kafka/Rabbit/Redis) would add an
ops dependency for a single-process workload and is ruled out by `AGENTS.md` §4.

## Decision

**A bounded in-process queue: one buffered channel + a fixed worker pool.**

- `queue_capacity` default **1024** events; `workers` default **8**
  goroutines. Both env-configurable. Nothing else in the process may spawn
  per-event goroutines.
- Enqueue is **non-blocking**. Flow: claim (ADR-0004) → persist sanitized audit
  metadata → non-blocking enqueue →
  ACK. New audit-write failures never receive success. If the buffer is full
  the handler releases the claim before admission and answers
  **503 + `Retry-After`** so the sender can retry; rejection increments a metric
  and is logged (sanitized).
- Workers own the full job lifecycle: process → mark `sent` / `duplicate` /
  `failed` (ADR-0004 rules), always with the `claimed_by` ownership guard.
- **Retries inside the worker**: read-only operations may retry classified
  recoverable failures. Every mutating retry requires proof the effect did not
  happen and that a prior owner/request cannot still act. Network errors, 5xx,
  429 and timeouts alone do not provide that proof;
  exponential backoff 2s → ×2 → cap 60s, ±20% jitter, max 3 attempts (config).
  4xx → immediate `failed`. **Unknown send outcome → `failed/unconfirmed_send`,
  never retried** (double-send prevention, ADR-0004 §5).
- **Graceful shutdown** (SIGTERM/SIGINT): HTTP server stops accepting; intake
  closes; workers drain in-flight jobs up to `drain_timeout` default **30s**;
  then contexts cancel. Unfinished jobs stay `processing` in MySQL. The boot
  scan takes ownership for reconciliation, not automatic replay: a crash may
  occur after external acceptance. The queue is **lossy**; persisted audit data
  does not reconstruct stripped content/media. Lossless durable-work recovery
  needs a separate engine design gate; do not claim it is implemented.
- Outbound HTTP concurrency is additionally bounded at the transport level
  (`http.Transport.MaxConnsPerHost`, per-client timeouts), so workers cannot
  open unbounded sockets to EVO/Chatwoot.
- Metrics (low cardinality): `bridge_queue_depth`, `bridge_queue_capacity`,
  `bridge_queue_rejected_total`, `bridge_jobs_total{outcome}`,
  `bridge_job_duration_seconds`.

## Consequences

- (+/~60 lines of stdlib, no broker, trivially testable; backpressure is a
  first-class, observable behaviour instead of “memory grows until OOM”.
- (−) **No cross-instance work distribution**: each instance processes its own
  enqueued events; the claim (ADR-0004) arbitrates duplicates between instances.
  Acceptable: deployment is 1–2 instances; when that changes, an outbox-type
  relay can be added behind the same `worker.Enqueue` seam (upgrade path).
- (−) No ordering guarantee between events of the same conversation (parallel
  workers; the legacy had none either). If ordered delivery is ever required it
  becomes a per-conversation keyed queue — not now (YAGNI).
- (−) 503-on-overflow depends on the sender retrying. Evolution GO/Chatwoot
  retry behaviour is **unverified** `[needs live validation]` — revisit after
  the first real-payload contract tests; until then, sizing the buffer
  generously (1024) and alerting on rejections is the mitigation.
- Risk: MEDIUM — an overflow window that drops non-retrying senders; monitored
  and bounded by `bridge_queue_rejected_total` + alert.

## Alternatives

Recovery clarification accepted 2026-10-11: ADR-0004 Amendment 3 and its design
validation override earlier implications that stale jobs can safely be replayed.
5xx/429/network failures alone do not prove a mutating call had no effect.
Never release an admitted claim on request cancellation; the worker may be acting.

- **External broker** — rejected: ops burden without a workload that needs it.
- **Unbounded goroutine per webhook (legacy)** — rejected: memory exhaustion
  under burst; no retry control.
- **Synchronous processing inside the webhook handler** — rejected: ACK latency
  would track EVO/Chatwoot latency and timeouts would lose events.
- **DB-as-queue** (`SELECT ... FOR UPDATE SKIP LOCKED` polling) — reconsidered
  and kept as a fallback if multi-instance distribution is needed; today it
  adds polling load and latency for no benefit.
