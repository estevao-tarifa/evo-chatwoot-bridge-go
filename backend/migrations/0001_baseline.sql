-- 0001_baseline.sql — legacy schema baseline (evo-chatwoot-bridge v5.4.4).
--
-- Reconstructs, byte-for-byte, the 8 tables the legacy Node/drizzle
-- installation used. Source of truth: legacy repo `drizzle/schema.ts` +
-- `drizzle/0000..0007*.sql`, documented in docs/audit/data-model.md.
--
-- All statements are CREATE TABLE IF NOT EXISTS:
--   * fresh database  → full schema created;
--   * legacy database → no-op, except `bridge_settings`, which no legacy
--     migration ever created (present only in schema.ts, created out-of-band
--     on some installs) — this file is the documented drift-heal path
--     (ADR-0002 "Consequences"). Running it on an existing database cannot
--     change existing tables.
--
-- Fidelity rules (ADR-0002):
--   * no foreign keys and no secondary indexes — the legacy schema had none;
--   * constraint names mirror drizzle's generated ones so SHOW CREATE TABLE
--     converges with a legacy database;
--   * legacy columns keep TIMESTAMP/NULLability/defaults exactly as drizzle
--     emitted them; only NEW tables use DATETIME(6).
--
-- ROLLBACK (manual, operator-only): DROP the 8 tables. DESTROYED DATA cannot
-- be recovered; requires a fresh mysqldump + explicit operator approval and is
-- never executed by the runner (ADR-0002 §6). Preferred recovery: forward fix.

CREATE TABLE IF NOT EXISTS `users` (
	`id` int AUTO_INCREMENT NOT NULL,
	`openId` varchar(64) NOT NULL,
	`name` text,
	`email` varchar(320),
	`loginMethod` varchar(64),
	`role` enum('user','admin') NOT NULL DEFAULT 'user',
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	`lastSignedIn` timestamp NOT NULL DEFAULT (now()),
	CONSTRAINT `users_id` PRIMARY KEY(`id`),
	CONSTRAINT `users_openId_unique` UNIQUE(`openId`)
);

-- Column order note: `connections` columns are written in the order a real
-- legacy database physically has them (drizzle ALTERs append columns at the
-- end), not in schema.ts declaration order — so SHOW CREATE TABLE converges.
CREATE TABLE IF NOT EXISTS `connections` (
	`id` int AUTO_INCREMENT NOT NULL,
	`name` varchar(255) NOT NULL,
	`evolutionApiUrl` varchar(512) NOT NULL,
	`evolutionApiKey` varchar(512) NOT NULL,
	`evolutionInstanceName` varchar(255) NOT NULL,
	`chatwootApiUrl` varchar(512) NOT NULL,
	`chatwootApiToken` varchar(512) NOT NULL,
	`chatwootAccountId` int NOT NULL,
	`chatwootInboxId` int,
	`isActive` boolean NOT NULL DEFAULT true,
	`webhookConfigured` boolean NOT NULL DEFAULT false,
	`createdBy` int,
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	`evolutionInstanceToken` varchar(512),
	`chatwootInboxName` varchar(255),
	`ignoreGroups` boolean DEFAULT true NOT NULL,
	`signMessages` boolean DEFAULT false NOT NULL,
	`autoReadMessages` boolean DEFAULT false NOT NULL,
	`rejectCalls` boolean DEFAULT false NOT NULL,
	`rejectCallMessage` varchar(500),
	`inboxGreetingEnabled` boolean DEFAULT false NOT NULL,
	`inboxGreetingMessage` text,
	`inboxAutoAssignment` boolean DEFAULT true NOT NULL,
	`inboxCsatEnabled` boolean DEFAULT false NOT NULL,
	`inboxWorkingHoursEnabled` boolean DEFAULT false NOT NULL,
	`inboxOutOfOfficeMessage` text,
	`inboxTimezone` varchar(100) DEFAULT 'America/Sao_Paulo',
	`inboxAllowAfterResolved` boolean DEFAULT true NOT NULL,
	`inboxLockSingleConversation` boolean DEFAULT false NOT NULL,
	`inboxSenderNameType` varchar(20) DEFAULT 'friendly',
	`inboxBusinessName` varchar(255),
	`n8nWebhookUrl` varchar(512),
	CONSTRAINT `connections_id` PRIMARY KEY(`id`)
);

CREATE TABLE IF NOT EXISTS `contacts_map` (
	`id` int AUTO_INCREMENT NOT NULL,
	`connectionId` int NOT NULL,
	`whatsappPhone` varchar(50) NOT NULL,
	`whatsappName` varchar(255),
	`whatsappProfilePic` text,
	`chatwootContactId` int NOT NULL,
	`chatwootConversationId` int,
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	CONSTRAINT `contacts_map_id` PRIMARY KEY(`id`)
);

CREATE TABLE IF NOT EXISTS `message_logs` (
	`id` int AUTO_INCREMENT NOT NULL,
	`connectionId` int NOT NULL,
	`direction` enum('whatsapp_to_chatwoot','chatwoot_to_whatsapp') NOT NULL,
	`messageType` varchar(50) NOT NULL,
	`status` enum('received','processing','sent','failed') NOT NULL,
	`sourceMessageId` varchar(255),
	`destinationMessageId` varchar(255),
	`contactPhone` varchar(50),
	`contactName` varchar(255),
	`contentPreview` text,
	`errorMessage` text,
	`rawPayload` json,
	`processedAt` timestamp,
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	CONSTRAINT `message_logs_id` PRIMARY KEY(`id`)
);

CREATE TABLE IF NOT EXISTS `admin_users` (
	`id` int AUTO_INCREMENT NOT NULL,
	`username` varchar(100) NOT NULL,
	`passwordHash` varchar(255) NOT NULL,
	`displayName` varchar(255),
	`isActive` boolean NOT NULL DEFAULT true,
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	`lastLoginAt` timestamp,
	CONSTRAINT `admin_users_id` PRIMARY KEY(`id`),
	CONSTRAINT `admin_users_username_unique` UNIQUE(`username`)
);

CREATE TABLE IF NOT EXISTS `notification_alerts` (
	`id` int AUTO_INCREMENT NOT NULL,
	`connectionId` int NOT NULL,
	`channel` enum('whatsapp','email') NOT NULL,
	`alertType` varchar(50) NOT NULL,
	`message` text NOT NULL,
	`success` boolean NOT NULL DEFAULT false,
	`errorMessage` text,
	`failureCount` int NOT NULL DEFAULT 0,
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	CONSTRAINT `notification_alerts_id` PRIMARY KEY(`id`)
);

CREATE TABLE IF NOT EXISTS `notification_settings` (
	`id` int AUTO_INCREMENT NOT NULL,
	`enabled` boolean NOT NULL DEFAULT false,
	`failureThreshold` int NOT NULL DEFAULT 3,
	`cooldownMinutes` int NOT NULL DEFAULT 30,
	`whatsappEnabled` boolean NOT NULL DEFAULT false,
	`whatsappConnectionId` int,
	`whatsappAlertPhone` varchar(50),
	`emailEnabled` boolean NOT NULL DEFAULT false,
	`smtpHost` varchar(255),
	`smtpPort` int DEFAULT 587,
	`smtpUser` varchar(255),
	`smtpPass` varchar(512),
	`smtpSecure` boolean NOT NULL DEFAULT false,
	`emailFrom` varchar(320),
	`emailTo` varchar(320),
	`createdAt` timestamp NOT NULL DEFAULT (now()),
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	CONSTRAINT `notification_settings_id` PRIMARY KEY(`id`)
);

-- The table legacy forgot to migrate: defined in drizzle/schema.ts only, never
-- created by drizzle/0000..0007. On fresh Go installs this file creates it; on
-- legacy installs where it already exists it stays untouched (IF NOT EXISTS).
CREATE TABLE IF NOT EXISTS `bridge_settings` (
	`id` int AUTO_INCREMENT NOT NULL,
	`key` varchar(100) NOT NULL,
	`value` text,
	`updatedAt` timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
	CONSTRAINT `bridge_settings_id` PRIMARY KEY(`id`),
	CONSTRAINT `bridge_settings_key_unique` UNIQUE(`key`)
);
