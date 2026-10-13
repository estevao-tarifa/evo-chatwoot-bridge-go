package httpapi

import (
	"mime"
	"net/http"
)

// csrfHeader is the double-submit-style marker the SPA sends on every mutation
// (spec §1.1). Its presence proves the request was not a simple cross-site form
// post; combined with the CORS allowlist and SameSite it is the CSRF stance
// (ADR-0001 §6, ADR-0006 §5).
const csrfHeader = "X-CSRF"

// requireCSRF rejects a state-changing request that lacks the CSRF header
// (403 csrf), then one whose body is not declared as JSON (415). Spec §1.1
// requires a mutation to carry both the JSON content type and X-CSRF: 1; the
// header alone does not prove the body was parsed as JSON.
func (s *server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(csrfHeader) != "1" {
			csrfRejected(w, s.log(r))
			return
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			unsupportedMediaType(w, s.log(r))
			return
		}
		next(w, r)
	}
}

// isJSONContentType reports whether the Content-Type header names JSON. Any
// parameters (e.g. "application/json; charset=utf-8") are accepted.
func isJSONContentType(header string) bool {
	if header == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

// isMutation reports whether a method changes state and therefore needs CSRF.
func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut:
		return true
	default:
		return false
	}
}
