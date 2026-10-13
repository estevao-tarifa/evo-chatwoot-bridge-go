/**
 * Typed REST client for the bridge admin API.
 *
 * Usage: `import { api } from "@/api"` then `api.dashboard.stats()`.
 * No tRPC, no superjson, no backend module import — the SPA is decoupled
 * from the backend build (ADR-0001, ADR-0007).
 */
import { auth } from "./auth";
import { chatwoot } from "./chatwoot";
import { connections } from "./connections";
import { contacts } from "./contacts";
import { dashboard } from "./dashboard";
import { logs } from "./logs";
import { notifications } from "./notifications";
import { settings } from "./settings";

export const api = {
  auth,
  dashboard,
  connections,
  chatwoot,
  contacts,
  notifications,
  logs,
  settings,
};

export type Api = typeof api;

export * from "./types";
export { routeTemplates, path, API_PREFIX } from "./routes";
export {
  ApiClientError,
  LOGIN_PATH,
  apiBaseUrl,
  apiUrl,
  redirectToLogin,
  setUnauthorizedHandler,
  http,
} from "./client";
