import { ApiClientError, http } from "./client";
import { path, routeTemplates } from "./routes";
import type {
  ConfigureWebhookResult,
  Connection,
  ConnectionCreateRequest,
  ConnectionsListResponse,
  ConnectionTestResult,
  ConnectionUpdateRequest,
  ResolveTokenResult,
  WebhookStatus,
  WebhookUrlReveal,
} from "./types";

export const connections = {
  /**
   * The backend wraps the list as `{connections:[…]}` (spec §1.2). Unwrap here
   * so pages keep consuming a plain `Connection[]`; a bare-array response is a
   * contract violation and is surfaced as an error, not a silent `undefined`.
   */
  list: async (): Promise<Connection[]> => {
    const body = await http.get<ConnectionsListResponse>(
      routeTemplates.connections
    );
    if (!Array.isArray(body?.connections)) {
      throw new ApiClientError(
        "invalid_response",
        "connections.list: expected {connections:[…]}",
        0
      );
    }
    return body.connections;
  },

  getById: (id: number) =>
    http.get<Connection>(path(routeTemplates.connection, { id })),

  create: (body: ConnectionCreateRequest) =>
    http.post<Connection>(routeTemplates.connections, body),

  update: (id: number, body: ConnectionUpdateRequest) =>
    http.patch<Connection>(path(routeTemplates.connection, { id }), body),

  remove: (id: number) =>
    http.delete<{ success: true }>(path(routeTemplates.connection, { id })),

  test: (id: number) =>
    http.post<ConnectionTestResult>(path(routeTemplates.connectionTest, { id })),

  /** Resolves + persists the instance token server-side; the token is not returned. */
  resolveToken: (id: number) =>
    http.post<ResolveTokenResult>(
      path(routeTemplates.connectionResolveToken, { id })
    ),

  configureWebhook: (id: number, bridgeUrl: string) =>
    http.post<ConfigureWebhookResult>(
      path(routeTemplates.connectionWebhook, { id }),
      { bridgeUrl }
    ),

  webhookStatus: (id: number) =>
    http.get<WebhookStatus>(path(routeTemplates.connectionWebhookStatus, { id })),

  /** Full token-bearing URL. Explicit user action only; never persisted. */
  revealEvolutionWebhookUrl: (id: number) =>
    http.get<WebhookUrlReveal>(
      path(routeTemplates.connectionWebhookUrlEvolution, { id })
    ),

  revealChatwootWebhookUrl: (id: number) =>
    http.get<WebhookUrlReveal>(
      path(routeTemplates.connectionWebhookUrlChatwoot, { id })
    ),
};
