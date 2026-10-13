/**
 * Single source of truth for every REST path the SPA calls.
 *
 * The Phase 5 contract (§1.2 mapping table + §6.5 the accepted decisions) and,
 * once T5 lands, `backend/api/openapi.yaml`. `tests/routes-contract.test.ts` asserts these
 * templates appear in that spec, so the client cannot drift from the contract.
 *
 * Paths are templates: `{param}` placeholders are filled by `path()`.
 */
export const API_PREFIX = "/api/v1";

export const routeTemplates = {
  // Auth / session (public)
  authState: `${API_PREFIX}/auth/state`,
  authSetup: `${API_PREFIX}/auth/setup`,
  authLogin: `${API_PREFIX}/auth/login`,
  authLogout: `${API_PREFIX}/auth/logout`,
  authMe: `${API_PREFIX}/auth/me`,

  // Dashboard (session)
  dashboardStats: `${API_PREFIX}/dashboard/stats`,
  dashboardMessagesPerHour: `${API_PREFIX}/dashboard/messages-per-hour`,
  dashboardMessagesPerDay: `${API_PREFIX}/dashboard/messages-per-day`,
  dashboardMessageTypes: `${API_PREFIX}/dashboard/message-types`,

  // Connections (session)
  connections: `${API_PREFIX}/connections`,
  connection: `${API_PREFIX}/connections/{id}`,
  connectionTest: `${API_PREFIX}/connections/{id}/test`,
  connectionResolveToken: `${API_PREFIX}/connections/{id}/resolve-token`,
  connectionWebhook: `${API_PREFIX}/connections/{id}/webhook`,
  connectionWebhookStatus: `${API_PREFIX}/connections/{id}/webhook-status`,
  // §6.5 R2: the only two endpoints allowed to return the full (token-bearing) URL.
  connectionWebhookUrlEvolution: `${API_PREFIX}/connections/{id}/webhook-url/evolution`,
  connectionWebhookUrlChatwoot: `${API_PREFIX}/connections/{id}/webhook-url/chatwoot`,
  connectionContactsSync: `${API_PREFIX}/connections/{id}/contacts/sync`,
  connectionInboxSettings: `${API_PREFIX}/connections/{id}/chatwoot/inbox-settings`,
  connectionChatwootWebhook: `${API_PREFIX}/connections/{id}/chatwoot/webhook`,

  // Chatwoot probes (session)
  chatwootInboxesQuery: `${API_PREFIX}/chatwoot/inboxes/query`,
  chatwootInboxes: `${API_PREFIX}/chatwoot/inboxes`,

  // Notifications (session)
  notificationsSettings: `${API_PREFIX}/notifications/settings`,
  notificationsAlerts: `${API_PREFIX}/notifications/alerts`,
  notificationsFailureStates: `${API_PREFIX}/notifications/failure-states`,

  // Logs / settings (session)
  logs: `${API_PREFIX}/logs`,
  settingsBridgeUrl: `${API_PREFIX}/settings/bridge-url`,
} as const;

export type RouteTemplate = (typeof routeTemplates)[keyof typeof routeTemplates];

/** Fill `{param}` placeholders in a route template. */
export function path(
  template: string,
  params: Record<string, string | number> = {}
): string {
  return template.replace(/\{(\w+)\}/g, (_match, key: string) => {
    const value = params[key];
    if (value === undefined) {
      throw new Error(`missing route param "${key}" for ${template}`);
    }
    return encodeURIComponent(String(value));
  });
}
