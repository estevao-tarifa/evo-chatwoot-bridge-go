import { http } from "./client";
import { routeTemplates } from "./routes";
import type {
  AuthMe,
  AuthSetupRequest,
  AuthState,
  AuthSuccess,
  AuthCredentials,
} from "./types";

export const auth = {
  state: () => http.get<AuthState>(routeTemplates.authState),

  setup: (body: AuthSetupRequest) =>
    http.post<AuthSuccess>(routeTemplates.authSetup, body),

  login: (body: AuthCredentials) =>
    http.post<AuthSuccess>(routeTemplates.authLogin, body),

  logout: () => http.post<{ success: true }>(routeTemplates.authLogout),

  me: () => http.get<AuthMe>(routeTemplates.authMe),
};
