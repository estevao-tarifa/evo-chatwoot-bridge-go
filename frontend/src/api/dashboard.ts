import { http } from "./client";
import { routeTemplates } from "./routes";
import type {
  DashboardStats,
  MessageTypeRow,
  MessagesPerDayRow,
  MessagesPerHourRow,
} from "./types";

export const dashboard = {
  stats: () => http.get<DashboardStats>(routeTemplates.dashboardStats),
  messagesPerHour: () =>
    http.get<MessagesPerHourRow[]>(routeTemplates.dashboardMessagesPerHour),
  messagesPerDay: () =>
    http.get<MessagesPerDayRow[]>(routeTemplates.dashboardMessagesPerDay),
  messageTypes: () =>
    http.get<MessageTypeRow[]>(routeTemplates.dashboardMessageTypes),
};
