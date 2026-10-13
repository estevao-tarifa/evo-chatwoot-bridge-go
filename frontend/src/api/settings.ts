import { http } from "./client";
import { routeTemplates } from "./routes";
import type { BridgeUrl } from "./types";

export const settings = {
  /** Read-only in Phase 5: `settings.setBridgeUrl` was dropped (§1.3). */
  getBridgeUrl: () => http.get<BridgeUrl>(routeTemplates.settingsBridgeUrl),
};
