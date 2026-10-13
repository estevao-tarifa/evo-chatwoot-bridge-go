import { http } from "./client";
import { path, routeTemplates } from "./routes";
import type { ContactsSyncResult } from "./types";

export const contacts = {
  sync: (connectionId: number) =>
    http.post<ContactsSyncResult>(
      path(routeTemplates.connectionContactsSync, { id: connectionId })
    ),
};
