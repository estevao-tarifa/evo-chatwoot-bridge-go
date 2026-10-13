# ADR-0004 — Persistent idempotency via `processed_messages`

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-0002 (this is the second migration, `0002_processed_messages`),
  ADR-005 (worker recovery uses the claim rows)

## Context

The legacy dedup is two in-memory caches (5-minute TTL) plus
`extractSentMessageId()` sniffing the Evolution GO send response; restart wipes
them, and if EVO’s response format differs the cache is never populated (H4 in
the rewrite assessment). The result is duplicate forwards and re-send loops —
one of the two failure classes the mission must eliminate (rule 6 of the identity
integrity rules). The rewrite also runs (potentially) as more than one instance:
the plan requires `INSERT` atomicity to decide the winner (§6, §8). Neither
in-memory TTL nor content hashing survives those constraints.

Key identity facts: the key is **never derived from message content**; it is
`(connection_id, source_system, event_type, source_message_id)`. `event_type` is
part of the key because IDs are namespaced by event kind (e.g. a reaction or
receipt references the ID of the message it targets — without `event_type` those
would collide with the message itself).

## Decision

**New table `processed_messages` is the single source of truth for “have I seen
and what happened to this event”.**

```sql
CREATE TABLE processed_messages (
  id                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  connection_id          INT UNSIGNED NOT NULL,
  source_system          ENUM('evolution','chatwoot') NOT NULL,
  event_type             VARCHAR(64) NOT NULL,
  source_message_id      VARCHAR(255) NOT NULL,
  status                 ENUM('processing','sent','failed','duplicate') NOT NULL,
  attempt                TINYINT UNSIGNED NOT NULL DEFAULT 1,
  claimed_by             CHAR(32) NULL,          -- instance + claim token
  claimed_at             DATETIME(6) NULL,
  destination_message_id VARCHAR(255) NULL,      -- ID returned by the destination API
  error_code             VARCHAR(64) NULL,       -- sanitized, never raw text
  sanitized_payload      JSON NULL,              -- media stripped (no base64)
  created_at             DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at             DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                                  ON UPDATE CURRENT_TIMESTAMP(6),
  expires_at             DATETIME(6) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_processed_claim (connection_id, source_system, event_type, source_message_id),
  KEY idx_processed_expires (expires_at),
  KEY idx_processed_dest (connection_id, destination_message_id),
  KEY idx_processed_stale (status, claimed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

Mechanics:

1. **Atomic claim.** `INSERT ... ON DUPLICATE KEY UPDATE status = status`
   (a deliberate no-op update). Affected-rows: `1` = inserted → this caller owns
   the event (winner); `0` = duplicate → no state changed. DSN must NOT set
   `clientFoundRows=true` (it changes these semantics) — asserted by a
   concurrency test.
   New work persists claim plus sanitized audit metadata **before ACK**. Audit
   write or queue admission failure never receives success. Only before admission,
   when no worker can act, may the winner release the claim with
   `DELETE ... WHERE id = ? AND claimed_by = ? AND status = 'processing'` so a
   sender retry can re-claim. Failed release remains non-2xx and preserves the row
   for reconciliation. Never release on request cancellation after admission.
2. **0 affected rows → read + decide** (each step atomic, no reliance on ODKU
   multi-assignment evaluation order — MySQL evaluates assignments left-to-right
   and later assignments see updated values, a trap we refuse):
   - `sent` / `duplicate` → **duplicate verdict**: drop, log outcome
     `duplicate` in `message_logs`, row untouched.
   - `processing` with fresh `claimed_at` → in-flight elsewhere → duplicate.
    - `processing` **stale** → guarded takeover for reconciliation only:
     `UPDATE ... SET claimed_by=?, claimed_at=NOW(6), status='processing', attempt=attempt+1 WHERE id=? AND status='processing' AND claimed_at < ?`;
      1 row → reconciliation owner, never permission to resend;
      0 rows → lost the race → duplicate. Unknown crash outcomes quarantine.
   - `failed` → terminal unless an explicit retry policy flips it (operator
     action or the future retry endpoint); re-delivery does NOT resurrect it.
3. **States.** `processing | sent | failed | duplicate`.
   `processing → sent|failed|duplicate` is the normal lifecycle.
   `duplicate` closes claims that were intentionally **not delivered** because
   the event is bridge-originated/echo traffic (loop protection, rule 6). A
   re-delivery of a terminal row never mutates it — the observation is recorded
   in `message_logs`.
4. **Ownership guard.** Every completion update carries
    `WHERE id = ? AND claimed_by = ? AND status = 'processing'`; a slow original
    owner that wakes up after a takeover writes nothing. This fences database
    mutations, not external HTTP effects; recovery never starts another send.
5. **Unknown send outcome** (timeout on the send call): the row moves to
   `failed` with `error_code = 'unconfirmed_send'` and is **never auto-retried**
   — blind retry risks a double send. Reconciliation is a manual/admin step
   (plan §6). This is a hard rule, tested.
6. **TTL.** `expires_at` default **7 days** (configurable). Cleanup: hourly
    in-process ticker deleting in batches of at most 1000, restricted to expired
    terminal rows excluding unresolved `failed/unconfirmed_send`. Never delete
    processing rows or unresolved unknown outcomes. The window must exceed the upstream re-delivery
   horizon of Evolution GO/Chatwoot retries — that horizon is unverified
   `[needs live validation]`; 7 days is deliberately generous.
7. **Loop protection for outgoing messages** uses the same table:
   when the bridge sends via Evolution, the returned message ID is stored in
   `destination_message_id` (and in the local cache). When an `IsFromMe` echo
   arrives, the engine resolves `(connection_id, destination_message_id)` via
   `idx_processed_dest` (or cache) → echo of our own send → `duplicate`, never
   re-forwarded. Exact Evolution send-response shape is unverified
   `[needs live validation]` (H4); if the ID cannot be extracted the row stays
    `failed/unconfirmed_send`. Destination-ID lookup cannot recognize an ID that
    was never persisted: verified correlation/routing is an engine-gate blocker.
8. **Restart recovery.** At boot the worker scans
   `status='processing' AND claimed_at < NOW(6) - INTERVAL stale_tx SECOND`
   (default 15 min — must exceed the slowest send including retries), and
    takes ownership for reconciliation only. A stale row cannot prove whether an
    external mutation began: quarantine as `failed/unconfirmed_send`, never
    replay automatically, even with a payload. Rows are never silently dropped.
    Automatic recovery of provably unstarted work needs a separate engine gate.
9. **Local cache is an optimisation only.** Bounded (≈4k entries), 10-minute
   TTL, may only short-circuit *known-terminal* outcomes (echo/destination-ID
   lookups); it never authorises delivery and losing it changes nothing but
   latency. This is the anti-H4 rule.
  10. **Stored payload safety**: `sanitized_payload` is allowlisted audit metadata,
     not a replay command. Never persist private content, base64, tokens, cookies,
     or credential-bearing URLs. Ingress owns sanitization; the repository checks
     JSON/64 KiB bounds. Whole-payload truncation markers are explicitly lossy.

## Consequences

- (+) One new-claim winner per retained row lifecycle across instances, with an
  audit trail. Pre-admission release and terminal cleanup permit future claims.
  Exactly-once external delivery
  is not implied; unknown crash outcomes require reconciliation, not resend.
- (+) One table answers “seen?”, “what happened?”, “when to give up?”.
- (−) Claim insertion plus sanitized audit persistence in the new-event ACK path
  (Design target: adds
  ≤ 5 ms p95 to ACK; see the ARCHITECTURE performance criteria). Monitoring: writes per
  second on `processed_messages`.
- (−) Resolved terminal rows expire; processing and unresolved unknown outcomes
  can grow without a TTL bound. Alert and reconcile them; never silently purge.
- Risk: MEDIUM — wrong `stale_tx`/`expires_at` tuning causes duplicate sends or
  lost retries; both are config with safe defaults and covered by concurrency
  tests (N simultaneous webhooks of the same event → exactly one delivery).

## Alternatives

- **In-memory only (legacy)** — rejected: loses on restart, not multi-instance
  safe (H4).
- **Content hash as key** — rejected: mutable/truncated content, media, edits.
- **Redis/queue-backed dedup** — rejected: extra runtime dependency for one
  table’s job; MySQL is already required.
- **UNIQUE on `message_logs`** — rejected: logs are per-attempt append-only by
  design, merging them with claim state muddles both.

## Amendment (2026-10-10)

Identity columns in `processed_messages` (`source_message_id`, `event_type`) are
compared **exactly**: they use `utf8mb4_bin` collation (byte-exact,
case-sensitive). The default `utf8mb4_0900_ai_ci` made the UNIQUE claim key
case/accent-insensitive, so ids differing only by case collapsed into one row and
a legitimate message was dropped as a duplicate (review M1). A follow-up numbered
migration applies the collation change; the table is new/empty in production, so
the change is free. `[needs live validation]` on whether real Evolution GO /
Chatwoot ids can differ only by case — identity comparison is exact regardless.

## Amendment 2 (2026-10-10) — claim ownership API (binding)

Independent QA found the repository did not enforce §4 (ownership) nor §2 (stale
takeover). Binding API for `internal/dedup`:

- `Claim` returns a **claim token** (`claimed_by`) alongside `(claimed, id)`.
- `Complete` / `Fail` / `MarkUnconfirmed` take the token and update with
  `WHERE id = ? AND claimed_by = ?`; a non-owner update affects 0 rows and must
  return a typed `ErrNotOwner` (never a silent no-op).
- Add `Reclaim(ctx, id, token, staleBefore)` that **atomically** takes over a
  stale `processing` row (`status='processing' AND claimed_at < staleBefore`),
  bumps `attempt`, sets the new owner, and reports whether it won. The worker
  uses it on restart; `RecoverStale` stays a lister only.
- Reject `ttl <= 0` and enforce a minimum ttl so a claim can never be born
  already expired.

## Amendment 3 (2026-10-11, design-reviewed and accepted)

The binding contract below is accepted
for this repository correction and supersedes earlier replay/completeness
claims. Reclaim is reconciliation-only; ownership guards do not fence external
sends. Retention floor is one hour (deployment default seven days); stale floor
fifteen minutes, enforced against the database clock. Preserve processing and
unresolved unknown-outcome tombstones during cleanup. Canonical tokens are 32
lowercase hex characters; reject trailing ASCII spaces in IDs rather than
normalize them (`utf8mb4_bin` uses PAD SPACE). No migration is authorized here.
Echo lookup still requires exact, scoped destination-ID comparison at the engine
gate.

`sanitized_payload` is allowlisted audit metadata, not a lossless replay command:
no private content, base64, tokens, cookies or credential-bearing URLs. Ingress
owns sanitization; repository validates JSON/64 KiB limits. Whole-payload
truncation markers are explicitly lossy. Claim plus audit storage is not durable
lossless work storage. ACK requires new audit write and queue admission; release
only before admission, never on cancellation after a worker could act.

The engine gate remains blocked until it validates privacy-safe replay-complete
work and a pre-effect boundary. Audit-only quarantine satisfies this repository
correction but cannot close the engine no-loss gate.
Existing loss/duplicate tests must not be weakened to declare that gate passed.
Quarantined work is visible pending reconciliation, not delivered work.
