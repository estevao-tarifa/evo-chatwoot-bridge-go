import { http } from "./client";
import { path, routeTemplates } from "./routes";
import type {
  ConfigureWebhookResult,
  CreateInboxRequest,
  Inbox,
  InboxSettingsRequest,
  InboxesQueryRequest,
  UpdateInboxSettingsResult,
} from "./types";

export const chatwoot = {
  /** Credentials travel in the POST body, never a query string (§1.3). */
  listInboxes: (body: InboxesQueryRequest) =>
    http.post<Inbox[]>(routeTemplates.chatwootInboxesQuery, body),

  createInbox: (body: CreateInboxRequest) =>
    http.post<Inbox>(routeTemplates.chatwootInboxes, body),

  updateInboxSettings: (connectionId: number, body: InboxSettingsRequest) =>
    http.patch<UpdateInboxSettingsResult>(
      path(routeTemplates.connectionInboxSettings, { id: connectionId }),
      body
    ),

  configureWebhook: (connectionId: number, bridgeUrl: string) =>
    http.post<ConfigureWebhookResult>(
      path(routeTemplates.connectionChatwootWebhook, { id: connectionId }),
      { bridgeUrl }
    ),
};
