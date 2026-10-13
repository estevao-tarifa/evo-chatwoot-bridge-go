import { http } from "./client";
import { routeTemplates } from "./routes";
import type {
  FailureStates,
  NotificationAlertsPage,
  NotificationSettings,
  NotificationSettingsUpdate,
} from "./types";

export const notifications = {
  getSettings: () =>
    http.get<NotificationSettings>(routeTemplates.notificationsSettings),

  /** `smtpPass` is write-only: absent = unchanged, non-empty = replace. */
  updateSettings: (body: NotificationSettingsUpdate) =>
    http.patch<NotificationSettings>(routeTemplates.notificationsSettings, body),

  listAlerts: (query: { connectionId?: number; limit?: number; offset?: number } = {}) =>
    http.get<NotificationAlertsPage>(routeTemplates.notificationsAlerts, {
      query,
    }),

  failureStates: () =>
    http.get<FailureStates>(routeTemplates.notificationsFailureStates),
};
