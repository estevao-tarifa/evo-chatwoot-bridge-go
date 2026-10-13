import { http } from "./client";
import { routeTemplates } from "./routes";
import type { LogsPage, LogsQuery } from "./types";

export const logs = {
  list: (query: LogsQuery = {}) =>
    http.get<LogsPage>(routeTemplates.logs, { query }),
};
