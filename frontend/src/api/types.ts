/**
 * Request/response models mirroring the Phase 5 REST contract
 * (frontend/backend mapping §1.2/§1.5, and the
 * canonical `backend/api/openapi.yaml` once T5 lands).
 *
 * Secret policy (§1.5, ADR-0006 Amendment 4): raw credentials are never
 * returned. Reads expose `has*` booleans; writes are write-only.
 */

// ── Error envelope ────────────────────────────────────────────────
export interface ApiErrorBody {
  error: { code: string; message: string };
}

// ── Auth ──────────────────────────────────────────────────────────
export type AuthStateValue = "needs_setup" | "ready";

export interface AuthState {
  state: AuthStateValue;
}

export interface AuthUser {
  id: number;
  username: string;
  displayName: string;
}

export interface AuthMe {
  user: AuthUser | null;
}

export interface AuthCredentials {
  username: string;
  password: string;
}

export interface AuthSetupRequest extends AuthCredentials {
  displayName?: string;
}

export interface AuthSuccess {
  success: true;
  user: { username: string; displayName: string };
}

// ── Dashboard ─────────────────────────────────────────────────────
export type MessageDirection = "whatsapp_to_chatwoot" | "chatwoot_to_whatsapp";
export type MessageStatus = "received" | "processing" | "sent" | "failed";

export interface MessageLog {
  id: number;
  connectionId?: number;
  direction: MessageDirection;
  status: MessageStatus;
  messageType: string;
  contactName?: string | null;
  contactPhone?: string | null;
  contentPreview?: string | null;
  sourceMessageId?: string | null;
  destinationMessageId?: string | null;
  errorMessage?: string | null;
  createdAt?: string | null;
  processedAt?: string | null;
  rawPayload?: unknown;
}

export interface DashboardStats {
  totalConnections: number;
  activeConnections: number;
  totalMessages: number;
  failedMessages: number;
  todayMessages: number;
  recentLogs: MessageLog[];
}

export interface MessagesPerHourRow {
  hour: string;
  incoming: number;
  outgoing: number;
  total: number;
}

export interface MessagesPerDayRow {
  day: string;
  incoming: number;
  outgoing: number;
  total: number;
}

export interface MessageTypeRow {
  type: string;
  count: number;
}

// ── Connections ───────────────────────────────────────────────────
export interface Connection {
  id: number;
  name: string;
  evolutionApiUrl: string;
  hasEvolutionApiKey: boolean;
  evolutionInstanceName: string;
  hasEvolutionInstanceToken: boolean;
  chatwootApiUrl: string;
  hasChatwootApiToken: boolean;
  chatwootAccountId: number;
  /** Backend sends a non-null `int`/`string` (`dto.go`): 0 / "" when unset. */
  chatwootInboxId: number;
  chatwootInboxName: string;
  isActive: boolean;
  ignoreGroups: boolean;
  signMessages: boolean;
  autoReadMessages: boolean;
  rejectCalls: boolean;
  rejectCallMessage: string | null;
  n8nWebhookUrl: string | null;
  webhookConfigured: boolean;
  inboxGreetingEnabled: boolean;
  inboxGreetingMessage: string | null;
  inboxAutoAssignment: boolean;
  inboxCsatEnabled: boolean;
  inboxWorkingHoursEnabled: boolean;
  inboxOutOfOfficeMessage: string | null;
  inboxTimezone: string | null;
  inboxAllowAfterResolved: boolean;
  inboxLockSingleConversation: boolean;
  inboxSenderNameType: string | null;
  inboxBusinessName: string | null;
  createdAt: string;
  updatedAt: string;
}

/**
 * `GET /api/v1/connections` wraps the list (spec §1.2 corrected):
 * `{connections:[…]}`, not a bare array. The API layer unwraps it.
 */
export interface ConnectionsListResponse {
  connections: Connection[];
}

/** Create body. Secrets are required on create (no stored value to keep). */
export interface ConnectionCreateRequest {
  name: string;
  evolutionApiUrl: string;
  evolutionApiKey: string;
  evolutionInstanceName: string;
  evolutionInstanceToken?: string;
  chatwootApiUrl: string;
  chatwootApiToken: string;
  chatwootAccountId: number;
  chatwootInboxId?: number | null;
  chatwootInboxName?: string | null;
  isActive?: boolean;
  ignoreGroups?: boolean;
  signMessages?: boolean;
  autoReadMessages?: boolean;
  rejectCalls?: boolean;
  rejectCallMessage?: string | null;
  n8nWebhookUrl?: string | null;
  inboxGreetingEnabled?: boolean;
  inboxGreetingMessage?: string | null;
  inboxAutoAssignment?: boolean;
  inboxCsatEnabled?: boolean;
  inboxWorkingHoursEnabled?: boolean;
  inboxOutOfOfficeMessage?: string | null;
  inboxTimezone?: string | null;
  inboxAllowAfterResolved?: boolean;
  inboxLockSingleConversation?: boolean;
  inboxSenderNameType?: string | null;
  inboxBusinessName?: string | null;
}

/**
 * Update body. Absent = unchanged, `null` = clear (non-secret only).
 * Secrets: absent = unchanged, non-empty string = replace; never cleared (§6.5 R8).
 */
export type ConnectionUpdateRequest = Partial<ConnectionCreateRequest>;

export interface ConnectionTestResult {
  success: boolean;
  evolution: { ok: boolean; error?: string };
  chatwoot: { ok: boolean; error?: string };
}

export interface ResolveTokenResult {
  success: true;
  hasToken: true;
}

/**
 * Configure responses return no URL (spec §6.5 R2). The Evolution endpoint
 * returns `{success:true}`; the Chatwoot one also echoes the inbox id. The only
 * token-bearing reveal is the two `/webhook-url` endpoints.
 */
export interface ConfigureWebhookResult {
  success: true;
  inboxId?: number;
}

export interface WebhookStatus {
  evolution: { configured: boolean; urlMasked: string | null; error?: string };
  chatwoot: { configured: boolean; urlMasked: string | null; error?: string };
  instanceStatus: string;
}

/**
 * Full, token-bearing URL reveal — explicit user action, never persisted (§6.5 R2).
 * The backend returns `{webhookUrl}`, not `{url}` (spec §1.2 corrected).
 */
export interface WebhookUrlReveal {
  webhookUrl: string;
}

export interface ContactsSyncResult {
  total: number;
  synced: number;
  skipped: number;
  failed: number;
  errors?: string[];
}

// ── Chatwoot ──────────────────────────────────────────────────────
export interface Inbox {
  id: number;
  name: string;
  channelType?: string;
  /** Token-bearing URL is masked in every inbox response; the only raw reveal
   * is the two `/webhook-url` endpoints (spec §6.5 R2). */
  webhookUrlMasked?: string;
}

export interface ChatwootCreds {
  chatwootApiUrl: string;
  chatwootApiToken: string;
  chatwootAccountId: number;
}

/** Either stored connection credentials or ad-hoc credentials (POST body only). */
export type InboxesQueryRequest =
  | { connectionId: number }
  | ChatwootCreds;

export interface InboxSettingsRequest {
  name?: string;
  greetingEnabled?: boolean;
  greetingMessage?: string | null;
  autoAssignment?: boolean;
  csatEnabled?: boolean;
  workingHoursEnabled?: boolean;
  outOfOfficeMessage?: string | null;
  timezone?: string;
  allowAfterResolved?: boolean;
  lockSingleConversation?: boolean;
  senderNameType?: "friendly" | "professional";
  businessName?: string | null;
}

export interface CreateInboxRequest extends InboxSettingsRequest {
  connectionId?: number;
  chatwootApiUrl?: string;
  chatwootApiToken?: string;
  chatwootAccountId?: number;
  inboxName: string;
  webhookUrl?: string;
}

export interface UpdateInboxSettingsResult {
  success: true;
  inbox: Inbox;
}

// ── Notifications ─────────────────────────────────────────────────
export interface NotificationSettings {
  enabled: boolean;
  failureThreshold: number;
  cooldownMinutes: number;
  whatsappEnabled: boolean;
  whatsappConnectionId: number | null;
  whatsappAlertPhone: string | null;
  emailEnabled: boolean;
  smtpHost: string | null;
  smtpPort: number | null;
  smtpUser: string | null;
  /** Raw password is never returned; only whether one is stored. */
  hasSmtpPass: boolean;
  smtpSecure: boolean;
  emailFrom: string | null;
  emailTo: string | null;
}

export interface NotificationSettingsUpdate {
  enabled?: boolean;
  failureThreshold?: number;
  cooldownMinutes?: number;
  whatsappEnabled?: boolean;
  whatsappConnectionId?: number | null;
  whatsappAlertPhone?: string | null;
  emailEnabled?: boolean;
  smtpHost?: string | null;
  smtpPort?: number | null;
  smtpUser?: string | null;
  /** Write-only: non-empty string = replace; absent = unchanged. */
  smtpPass?: string;
  smtpSecure?: boolean;
  emailFrom?: string | null;
  emailTo?: string | null;
}

export interface NotificationAlert {
  id: number;
  connectionId: number;
  channel: "whatsapp" | "email";
  failureCount: number;
  success: boolean;
  errorMessage?: string | null;
  createdAt: string;
}

export interface NotificationAlertsPage {
  alerts: NotificationAlert[];
  total: number;
}

export interface FailureState {
  consecutiveFailures: number;
  lastAlertAt: string | null;
}

export type FailureStates = Record<string, FailureState>;

// ── Logs / settings ───────────────────────────────────────────────
export type LogsQuery = {
  connectionId?: number;
  direction?: MessageDirection;
  status?: MessageStatus;
  search?: string;
  limit?: number;
  offset?: number;
};

export interface LogsPage {
  logs: MessageLog[];
  total: number;
}

export interface BridgeUrl {
  url: string | null;
  source: "database" | "env" | "none";
}
