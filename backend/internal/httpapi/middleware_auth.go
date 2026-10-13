package httpapi

import "net/http"

// requireSession enforces the ADR-0006 §1 mandate: every session route needs a
// valid cookie session. There is no configuration flag and no public fallback
// (the legacy publicProcedure hole is structurally absent). The resolved
// session is attached to the request context for the handler.
//
// When the admin surface is not wired (a no-database start) it fails closed
// with 401 rather than dereferencing a nil service into a recovered 500.
func (s *server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminReady() {
			unauthorized(w, s.log(r))
			return
		}
		token := sessionToken(r)
		sess, err := s.admin.Auth.Authenticate(r.Context(), token)
		if err != nil {
			unauthorized(w, s.log(r))
			return
		}
		next(w, r.WithContext(withSession(r.Context(), sess)))
	}
}
