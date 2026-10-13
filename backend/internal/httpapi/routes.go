package httpapi

import "net/http"

// AuthKind selects whether a route requires an admin session.
type AuthKind int

const (
	// AuthPublic routes are reachable without a session (auth/*, meta).
	AuthPublic AuthKind = iota
	// AuthSession routes require a valid cookie session (ADR-0006 §1).
	AuthSession
)

// routeHandler is the uniform handler shape. Method expressions such as
// (*server).authState satisfy it, so the table is a package-level value that
// both the router and the OpenAPI coverage test read (spec §2).
type routeHandler func(*server, http.ResponseWriter, *http.Request)

// Route is one entry of the admin API table.
type Route struct {
	Method  string
	Pattern string // ServeMux pattern path, e.g. "/api/v1/connections/{id}"
	Auth    AuthKind
	Handler routeHandler
}

// apiRoutes is the single source of truth for the admin surface. newHandler
// registers the ServeMux from this table and TestOpenAPIContractCoverage
// iterates the same table, so the router and the checked contract cannot drift.
var apiRoutes = []Route{
	// Ops/meta (public; the only /api/v1 route that is not part of the SPA auth gate).
	{Method: http.MethodGet, Pattern: "/api/v1/meta", Auth: AuthPublic, Handler: (*server).meta},

	// Auth / session (public).
	{Method: http.MethodGet, Pattern: "/api/v1/auth/state", Auth: AuthPublic, Handler: (*server).authState},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/setup", Auth: AuthPublic, Handler: (*server).authSetup},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/login", Auth: AuthPublic, Handler: (*server).authLogin},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/logout", Auth: AuthPublic, Handler: (*server).authLogout},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/me", Auth: AuthPublic, Handler: (*server).authMe},

	// Dashboard.
	{Method: http.MethodGet, Pattern: "/api/v1/dashboard/stats", Auth: AuthSession, Handler: (*server).dashboardStats},
	{Method: http.MethodGet, Pattern: "/api/v1/dashboard/messages-per-hour", Auth: AuthSession, Handler: (*server).dashboardMessagesPerHour},
	{Method: http.MethodGet, Pattern: "/api/v1/dashboard/messages-per-day", Auth: AuthSession, Handler: (*server).dashboardMessagesPerDay},
	{Method: http.MethodGet, Pattern: "/api/v1/dashboard/message-types", Auth: AuthSession, Handler: (*server).dashboardMessageTypes},

	// Connections.
	{Method: http.MethodGet, Pattern: "/api/v1/connections", Auth: AuthSession, Handler: (*server).connectionsList},
	{Method: http.MethodPost, Pattern: "/api/v1/connections", Auth: AuthSession, Handler: (*server).connectionCreate},
	{Method: http.MethodGet, Pattern: "/api/v1/connections/{id}", Auth: AuthSession, Handler: (*server).connectionGet},
	{Method: http.MethodPatch, Pattern: "/api/v1/connections/{id}", Auth: AuthSession, Handler: (*server).connectionUpdate},
	{Method: http.MethodDelete, Pattern: "/api/v1/connections/{id}", Auth: AuthSession, Handler: (*server).connectionDelete},
	{Method: http.MethodPost, Pattern: "/api/v1/connections/{id}/test", Auth: AuthSession, Handler: (*server).connectionTest},
	{Method: http.MethodPost, Pattern: "/api/v1/connections/{id}/resolve-token", Auth: AuthSession, Handler: (*server).connectionResolveToken},
	{Method: http.MethodPost, Pattern: "/api/v1/connections/{id}/webhook", Auth: AuthSession, Handler: (*server).connectionConfigureWebhook},
	{Method: http.MethodGet, Pattern: "/api/v1/connections/{id}/webhook-status", Auth: AuthSession, Handler: (*server).connectionWebhookStatus},
	{Method: http.MethodPost, Pattern: "/api/v1/connections/{id}/contacts/sync", Auth: AuthSession, Handler: (*server).connectionContactsSync},
	{Method: http.MethodGet, Pattern: "/api/v1/connections/{id}/webhook-url/evolution", Auth: AuthSession, Handler: (*server).connectionWebhookURLEvolution},
	{Method: http.MethodGet, Pattern: "/api/v1/connections/{id}/webhook-url/chatwoot", Auth: AuthSession, Handler: (*server).connectionWebhookURLChatwoot},

	// Chatwoot.
	{Method: http.MethodPost, Pattern: "/api/v1/chatwoot/inboxes/query", Auth: AuthSession, Handler: (*server).chatwootInboxesQuery},
	{Method: http.MethodPost, Pattern: "/api/v1/chatwoot/inboxes", Auth: AuthSession, Handler: (*server).chatwootInboxCreate},
	{Method: http.MethodPatch, Pattern: "/api/v1/connections/{id}/chatwoot/inbox-settings", Auth: AuthSession, Handler: (*server).chatwootInboxSettings},
	{Method: http.MethodPost, Pattern: "/api/v1/connections/{id}/chatwoot/webhook", Auth: AuthSession, Handler: (*server).chatwootConfigureWebhook},

	// Notifications.
	{Method: http.MethodGet, Pattern: "/api/v1/notifications/settings", Auth: AuthSession, Handler: (*server).notificationsGetSettings},
	{Method: http.MethodPatch, Pattern: "/api/v1/notifications/settings", Auth: AuthSession, Handler: (*server).notificationsUpdateSettings},
	{Method: http.MethodGet, Pattern: "/api/v1/notifications/alerts", Auth: AuthSession, Handler: (*server).notificationsAlerts},
	{Method: http.MethodGet, Pattern: "/api/v1/notifications/failure-states", Auth: AuthSession, Handler: (*server).notificationsFailureStates},

	// Logs / settings.
	{Method: http.MethodGet, Pattern: "/api/v1/logs", Auth: AuthSession, Handler: (*server).logsList},
	{Method: http.MethodGet, Pattern: "/api/v1/settings/bridge-url", Auth: AuthSession, Handler: (*server).settingsBridgeURL},
}

// registerRoutes registers every apiRoutes entry on mux, wrapped with the
// per-route auth and CSRF middleware. Session is checked before CSRF so an
// unauthenticated mutation answers 401 (not 403).
func registerRoutes(mux *http.ServeMux, s *server) {
	for _, rt := range apiRoutes {
		h := rt.Handler
		var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
			h(s, w, r)
		}
		if isMutation(rt.Method) {
			handler = s.requireCSRF(handler)
		}
		if rt.Auth == AuthSession {
			handler = s.requireSession(handler)
		}
		mux.Handle(rt.Method+" "+rt.Pattern, handler)
	}
}
