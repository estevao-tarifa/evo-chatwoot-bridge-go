# ADR-0002 — Preserve the legacy MySQL schema; new numbered forward-only migrations

- Status: Accepted
- Date: 2026-10-10
- Related: ADR-0004 (`processed_messages` is the first new table), ADR-0006
  (`connections.webhook_token` is an additive column)

## Context

The Go rewrite shares the production MySQL 8 database with the legacy
Node/drizzle installation. Verified facts from the legacy repo (commit
`daeb04b0`, v5.4.4):

- **8 tables in use**: `users`, `connections`, `message_logs`, `contacts_map`,
  `admin_users`, `notification_settings`, `notification_alerts`,
  `bridge_settings` (7 functional + `users` kept for compatibility).
- Migration tracking is drizzle's `__drizzle_migrations` (`hash`, `created_at`).
  Legacy `autoMigrate.ts` handles three scenarios: fresh install, install with
  tracking, and **install without tracking** (tables created manually — it then
  fabricates the tracking table). So prod state varies; the actual state of the
  production database could not be observed (no DB access) `[needs live validation]`.
- **`bridge_settings` has no tracked migration at all** (absent from
  `drizzle/*.sql`; only defined in `drizzle/schema.ts`) — schema drift created
  manually or via `drizzle-kit push`. It is expected to exist in production
  (the app reads/writes it) but this could not be verified `[needs live validation]`.
- The legacy DDL has **no secondary indexes** (only PKs and two UNIQUEs:
  `users.openId`, `admin_users.username`).
- The original repo is read-only; MySQL access is only available during the pilot
  homologation — migrations are validated with fixtures and a MySQL 8 CI
  container.
- Non-negotiables (`AGENTS.md` §8): no destructive operations on production;
  migrations forward-only; `__drizzle_migrations` must not be touched.

## Decision

1. **Schema ownership moves to `backend/migrations/NNNN_<slug>.sql`** — small,
   numbered, forward-only SQL files, one logical change each. A Go runner
   embedded in the binary applies them at startup (parity with legacy
   `autoMigrate`), with retry/backoff for a not-yet-ready DB, serialized by
   `GET_LOCK('evo_bridge_migrate', 10)` so two instances cannot race. Flags:
   `-migrate-only` (run and exit) and `-skip-migrate` (ops override).
2. **Tracking table `schema_migrations`** (owned by us): `filename`, `sha256`,
   `applied_at`. Applied files are never re-run; a changed checksum for an
   applied file is a startup error. **`__drizzle_migrations` is never read,
   written or dropped** — it stays as the legacy record.
3. **Baseline `0001_baseline.sql`** recreates the 8 tables as of v5.4.4
   (reconstructed from `drizzle/*.sql` + `schema.ts`, which is why
   `bridge_settings` is included), all statements `CREATE TABLE IF NOT EXISTS`.
   Runner rule:
   - fresh/empty DB → apply 0000, then increments;
   - legacy DB (`connections` exists) → execute the baseline (every statement is
     `CREATE TABLE IF NOT EXISTS`) and record it; this heals the `bridge_settings`
     drift (see Amendment below)
     it (idempotent `IF NOT EXISTS` means the DDL would be a no-op anyway, but
     skipping keeps prod DDL out of startup);
   - partial/unknown state → **fail loudly** for manual review (no guessing).
4. **Increments are additive.** Planned set (non-exhaustive):
   `0002_processed_messages` (ADR-0004), `0003_contacts_map_index`,
   `0004_processed_messages_exact_collation`, later
   `0005_message_logs_additive_columns`
   (`trace_id`, `attempt`, `error_code`, `outcome` — `AGENTS.md` §7 / plan §7),
   `0003_connections_webhook_token` (ADR-0006), `0004_indexes` (see below).
   Non-unique lookup indexes are added freely; a **UNIQUE on
   `contacts_map(connectionId, whatsappPhone)` is only added after H2 is proven
   and pre-existing duplicates are cleaned with operator approval** (a UNIQUE
   added on dirty data fails the migration; cleanup is a data fix, not a DDL
   step).
5. **MySQL 8 + `go-sql-driver/mysql` compatibility rules:**
   - DSN: `parseTime=true&loc=UTC&charset=utf8mb4&collation=utf8mb4_0900_ai_ci`,
     TCP keepalive/timeouts set; session `time_zone='+00:00'` so `NOW()` and
     `CURRENT_TIMESTAMP` are UTC (the legacy stack also stored UTC).
   - Runtime connection: `multiStatements=false`. A **separate migration
     connection** opens with `multiStatements=true` only to execute the embedded
     static SQL files (one file may contain many statements); it is closed after
     migrating. No dynamic SQL is ever built.
   - New tables use `datetime(6)` (UTC), not `TIMESTAMP` — MySQL `TIMESTAMP`
     dies in 2038 and the legacy tables already use it; we do not retro-fix
     legacy columns.
   - `ENUM` values may only be **appended** (online, `ALGORITHM=INPLACE,
     LOCK=NONE` in MySQL 8) and never reordered/removed.
   - `ALTER TABLE` statements carry explicit `ALGORITHM`/`LOCK` where
     applicable so a non-online change fails loudly in staging instead of
     locking production.
   - `utf8mb4` everywhere; index-prefix safety assumed at `innodb_large_prefix`
     defaults (MySQL 8, 3072-byte keys).
6. **Rollback strategy: forward-only by design.**
   - Every file ends with a commented `-- ROLLBACK (manual, operator-only):`
     block with the inverse SQL (drop added index/column/table). The runner
     never executes it; there are no down migrations.
   - Rolling back a destroyed-schema step is destructive → requires explicit
     operator approval, a fresh `mysqldump` and is only ever run by a human.
     New-table drops are the only low-risk rollback and still manual.
   - Preferred recovery in production is a **forward fix** (new numbered
     migration), not reversal.
7. **Verification per migration** (CI, no prod access needed): apply to (a) an
   empty MySQL 8 container and (b) a fixture DB that reproduces the legacy
   schema (dumped from `drizzle/*.sql`). Both paths must converge to the same
   schema (compared by `SHOW CREATE TABLE`).

## Consequences

- (+) The legacy schema is preserved byte-for-byte in production; only additive
  objects are created. Both codebases can technically coexist on the DB during
  the transition.
- (+) `bridge_settings` drift is healed by the `IF NOT EXISTS` baseline/heal
  path instead of crashing the app.
- (−) The runner needs DDL privileges at boot (legacy had the same). Mitigation:
  static embedded files, advisory lock, `-skip-migrate` for hardened
  environments.
- (−) Running legacy **and** Go bridges simultaneously on the same connection is
  forbidden (different dedup stores) — an operational rule for the rollout, not a
  schema problem.
- Risk: HIGH if a migration touches prod data (mitigated: additive-only in this
  phase, fixture CI, dry-run mode, explicit approval workflow). Risk of schema
  drift is LOW now that `schema_migrations` records checksums.

## Alternatives

- **Keep drizzle-kit for migrations** — requires Node/pnpm in the deploy
  pipeline forever; rejected (runtime is a static binary).
- **`golang-migrate` dependency** — real but heavy for our needs (our runner is
  ~150 lines, stdlib + driver only); rejected under the ADR-0003 budget.
- **Manual operator SQL** — the legacy drift (`bridge_settings`) is proof this
  fails; rejected.
- **Rebuild schema from scratch / dump-and-restore** — destroys production data
  guarantees; rejected.

## Amendment (2026-10-10, accepted by the maintainer)

The baseline migration is **executed** on legacy databases (every statement is
`CREATE TABLE IF NOT EXISTS`), not merely marked as applied. Rationale: ADR-0002
requires healing the `bridge_settings` drift, which is only possible if the
baseline actually runs; skipping it would leave a fresh legacy install broken.
`schema_migrations` tracks what the Go migrator applied; `__drizzle_migrations`
is never read or written. Convergence against the legacy drizzle DDL was proven
byte-identical on MySQL 8.0.46 (storage slice).

`DATABASE_URL` is parsed strictly: unknown query parameters are rejected loudly
(only `tls=true|skip-verify|preferred` allowed). mysql2-style params must be
mapped by the operator. `[needs live validation]` against the real
production URL.
