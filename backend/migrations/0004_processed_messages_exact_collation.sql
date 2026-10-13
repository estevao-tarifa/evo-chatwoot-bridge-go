-- 0004_processed_messages_exact_collation.sql — exact identity comparison.
--
-- ADR-0004 Amendment (review M1): the claim key
-- (connection_id, source_system, event_type, source_message_id) must compare
-- `source_message_id` and `event_type` byte-exactly. The default
-- utf8mb4_0900_ai_ci is case/accent-insensitive, so ids differing only by case
-- (e.g. "AbC" vs "abc") collapsed into one row and a legitimate message was
-- dropped as a duplicate. utf8mb4_bin is byte-exact and case-sensitive.
--
-- ALGORITHM=COPY is required and explicit: MySQL 8 cannot change a column
-- collation INPLACE (ER_ALTER_OPERATION_NOT_SUPPORTED: "Cannot change column
-- type INPLACE"). It is safe here because `processed_messages` is a new table,
-- empty in production when this runs (ADR-0004 Amendment) — the rebuild is
-- trivial. `connection_id`/`source_system`/`status` are unaffected.
--
-- This file is idempotent: re-applying the same MODIFY is a no-op, so the DDL
-- commit / tracking-insert crash window needs no special handling.
--
-- ROLLBACK (manual, operator-only) — restores the false-dedup bug, forward fix
-- is preferred:
--   ALTER TABLE `processed_messages`
--     MODIFY `event_type` VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL,
--     MODIFY `source_message_id` VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL;

ALTER TABLE `processed_messages`
	MODIFY `event_type` VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
	MODIFY `source_message_id` VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
	ALGORITHM=COPY, LOCK=SHARED;
