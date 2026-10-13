-- 0002_processed_messages.sql — persistent idempotency (ADR-0004).
--
-- Single source of truth for "have I seen this event and what happened to it".
-- The identity key is (connection_id, source_system, event_type,
-- source_message_id) — never derived from message content.
--
-- Atomic claim contract (ADR-0004 §1, covered by an integration test):
--   INSERT ... ON DUPLICATE KEY UPDATE status = status
--   affected rows 1 → inserted, this caller owns the event;
--   affected rows 0 → duplicate, no state changed.
-- The DSN must NOT set clientFoundRows=true (it changes this semantics); the
-- runtime DSN built by internal/storage/mysql does not.
--
-- New-table rules (ADR-0002 §5): DATETIME(6) instead of TIMESTAMP, UTC
-- (session time_zone is +00:00), explicit utf8mb4 collation.
--
-- ROLLBACK (manual, operator-only): DROP TABLE `processed_messages`;
-- new-table drops are the only low-risk rollback and are still manual
-- (ADR-0002 §6). Loses the idempotency audit trail.

CREATE TABLE IF NOT EXISTS `processed_messages` (
	`id`                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	`connection_id`          INT UNSIGNED NOT NULL,
	`source_system`          ENUM('evolution','chatwoot') NOT NULL,
	`event_type`             VARCHAR(64) NOT NULL,
	`source_message_id`      VARCHAR(255) NOT NULL,
	`status`                 ENUM('processing','sent','failed','duplicate') NOT NULL,
	`attempt`                TINYINT UNSIGNED NOT NULL DEFAULT 1,
	`claimed_by`             CHAR(32) NULL,
	`claimed_at`             DATETIME(6) NULL,
	`destination_message_id` VARCHAR(255) NULL,
	`error_code`             VARCHAR(64) NULL,
	`sanitized_payload`      JSON NULL,
	`created_at`             DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
	`updated_at`             DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	                                  ON UPDATE CURRENT_TIMESTAMP(6),
	`expires_at`             DATETIME(6) NOT NULL,
	PRIMARY KEY (`id`),
	UNIQUE KEY `uq_processed_claim` (`connection_id`, `source_system`, `event_type`, `source_message_id`),
	KEY `idx_processed_expires` (`expires_at`),
	KEY `idx_processed_dest` (`connection_id`, `destination_message_id`),
	KEY `idx_processed_stale` (`status`, `claimed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
