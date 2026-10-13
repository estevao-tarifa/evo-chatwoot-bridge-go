-- Legacy schema fixture — evo-chatwoot-bridge v5.4.4.
--
-- Verbatim copy of the legacy repo drizzle/0000..0007*.sql (source of truth,
-- read-only) with the drizzle "--> statement-breakpoint" markers stripped:
-- they are drizzle directives, not SQL. Used by TestMigrateLegacyDatabase to
-- reproduce a production database before the Go migrations run.
--
-- bridge_settings is intentionally absent: no legacy migration ever created
-- it (drizzle/schema.ts only), and the Go baseline heals that drift.

-- === drizzle/0000_plain_may_parker.sql ===
CREATE TABLE `users` (
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

-- === drizzle/0001_burly_gressill.sql ===
CREATE TABLE `connections` (
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
	CONSTRAINT `connections_id` PRIMARY KEY(`id`)
);

CREATE TABLE `contacts_map` (
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

CREATE TABLE `message_logs` (
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

-- === drizzle/0002_absurd_bullseye.sql ===
ALTER TABLE `connections` ADD `evolutionInstanceToken` varchar(512);
-- === drizzle/0003_rare_alex_power.sql ===
ALTER TABLE `connections` ADD `chatwootInboxName` varchar(255);
-- === drizzle/0004_large_puma.sql ===
ALTER TABLE `connections` ADD `ignoreGroups` boolean DEFAULT true NOT NULL;
ALTER TABLE `connections` ADD `signMessages` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `autoReadMessages` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `rejectCalls` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `rejectCallMessage` varchar(500);
ALTER TABLE `connections` ADD `inboxGreetingEnabled` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `inboxGreetingMessage` text;
ALTER TABLE `connections` ADD `inboxAutoAssignment` boolean DEFAULT true NOT NULL;
ALTER TABLE `connections` ADD `inboxCsatEnabled` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `inboxWorkingHoursEnabled` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `inboxOutOfOfficeMessage` text;
ALTER TABLE `connections` ADD `inboxTimezone` varchar(100) DEFAULT 'America/Sao_Paulo';
ALTER TABLE `connections` ADD `inboxAllowAfterResolved` boolean DEFAULT true NOT NULL;
ALTER TABLE `connections` ADD `inboxLockSingleConversation` boolean DEFAULT false NOT NULL;
ALTER TABLE `connections` ADD `inboxSenderNameType` varchar(20) DEFAULT 'friendly';
ALTER TABLE `connections` ADD `inboxBusinessName` varchar(255);
-- === drizzle/0005_absurd_lady_bullseye.sql ===
CREATE TABLE `admin_users` (
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

-- === drizzle/0006_wonderful_famine.sql ===
ALTER TABLE `connections` ADD `n8nWebhookUrl` varchar(512);
-- === drizzle/0007_neat_captain_universe.sql ===
CREATE TABLE `notification_alerts` (
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

CREATE TABLE `notification_settings` (
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
