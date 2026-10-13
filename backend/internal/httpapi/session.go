package httpapi

import (
	"context"
	"net/http"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"
)

// sessionCookieName is the legacy cookie name (ADR-0006 §1.4 parity).
const sessionCookieName = "app_session_id"

type sessionContextKey struct{}

// withSession returns a copy of ctx carrying the authenticated session.
func withSession(ctx context.Context, sess *auth.Session) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, sess)
}

// setSessionCookie writes the HttpOnly session cookie. Secure and SameSite come
// from configuration (ADR-0006 §1.4, ADR-0007 §5); Max-Age is 7 days.
func (s *server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.DefaultSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.admin.CookieSecure,
		SameSite: s.admin.CookieSameSite,
	})
}

// clearSessionCookie expires the session cookie (logout).
func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.admin.CookieSecure,
		SameSite: s.admin.CookieSameSite,
	})
}

// sessionToken reads the session cookie value, or "".
func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
