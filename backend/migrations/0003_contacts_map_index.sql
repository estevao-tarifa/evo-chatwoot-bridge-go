-- 0003_contacts_map_index.sql — additive lookup index for contacts_map.
--
-- Backs the per-connection lookups by WhatsApp identity
-- (findContactMap / findContactMapByChatwootConversation, audit data-model §4)
-- without changing any row.
--
-- The UNIQUE constraint on (connectionId, whatsappPhone) is intentionally
-- NOT added here: the legacy write path is SELECT-then-INSERT and races under
-- concurrent webhooks (server/db.ts), and production may already contain
-- duplicate mappings (audit R3/H2). A UNIQUE built on dirty data fails the
-- migration, and cleanup is a data fix that needs an operator-approved plan
-- (ADR-0002 §4). This migration adds only the non-unique index; the UNIQUE
-- will be a separate numbered migration after H2 is proven and duplicates are
-- cleaned.
--
-- ALGORITHM/LOCK are explicit on purpose: adding a secondary index is online
-- (INPLACE, LOCK=NONE) on MySQL 8 — if that ever stops being true the
-- migration fails loudly instead of locking production (ADR-0002 §5).
--
-- Note: MySQL DDL auto-commits. If the process dies between this statement
-- and the schema_migrations insert, re-running fails with "Duplicate key
-- name"; recovery is the rollback below + re-run, or the tracking row.

ALTER TABLE `contacts_map`
	ADD INDEX `idx_contacts_map_conn_phone` (`connectionId`, `whatsappPhone`),
	ALGORITHM=INPLACE, LOCK=NONE;

-- ROLLBACK (manual, operator-only):
-- ALTER TABLE `contacts_map` DROP INDEX `idx_contacts_map_conn_phone`;
