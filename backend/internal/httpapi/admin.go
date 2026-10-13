package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
)

// AdminDeps wires the Phase 5 admin surface. Every field is required in
// production; a nil dependency makes the affected handler fail closed.
type AdminDeps struct {
	Auth          *auth.Service
	Connections   admin.ConnectionsStore
	Logs          admin.LogsStore
	Dashboard     admin.DashboardStore
	Settings      admin.SettingsStore
	Notifications admin.NotificationsStore
	Upstream      admin.UpstreamFactory

	// BridgeURL is the public base URL used to build webhook URLs. Empty means
	// the URL cannot be built and the reveal endpoints answer invalid_request.
	BridgeURL string
	// WebhookSecret is WEBHOOK_TOKEN_SECRET; the derived per-connection token is
	// built from it. Empty means the reveal endpoints answer invalid_request.
	WebhookSecret string

	CookieSecure   bool
	CookieSameSite http.SameSite
}

// log returns the request-scoped logger (correlation id attached).
func (s *server) log(r *http.Request) *slog.Logger {
	return observability.FromContext(r.Context(), s.logger)
}

// adminReady reports whether the admin surface is wired.
func (s *server) adminReady() bool {
	return s.admin != nil && s.admin.Auth != nil
}
