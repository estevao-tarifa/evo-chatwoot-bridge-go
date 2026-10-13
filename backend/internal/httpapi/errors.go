package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
)

// Stable error codes for the /api/v1/* envelope (spec §1.1). The message is a
// static, safe string that never carries upstream/DB text.
const (
	codeUnauthorized         = "unauthorized"
	codeForbidden            = "forbidden"
	codeCSRF                 = "csrf"
	codeInvalidRequest       = "invalid_request"
	codeNotFound             = "not_found"
	codeConflict             = "conflict"
	codeRateLimited          = "rate_limited"
	codeRequestTooLarge      = "request_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeUpstreamError        = "upstream_error"
	codeInternal             = "internal"
)

// apiError is the body of every /api/v1/* failure.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

// writeAPIError writes the {error:{code,message}} envelope. It is the single
// shaping point for admin errors, so no handler can accidentally leak text.
func writeAPIError(logger *slog.Logger, w http.ResponseWriter, status int, code, message string) {
	writeJSON(logger, w, status, apiErrorEnvelope{Error: apiError{Code: code, Message: message}})
}

// Static messages keep the contract stable and leak nothing.
func unauthorized(w http.ResponseWriter, logger *slog.Logger) {
	writeAPIError(logger, w, http.StatusUnauthorized, codeUnauthorized, "authentication required")
}

func forbidden(w http.ResponseWriter, logger *slog.Logger, message string) {
	writeAPIError(logger, w, http.StatusForbidden, codeForbidden, message)
}

func csrfRejected(w http.ResponseWriter, logger *slog.Logger) {
	writeAPIError(logger, w, http.StatusForbidden, codeCSRF, "missing or invalid csrf header")
}

func invalidRequest(w http.ResponseWriter, logger *slog.Logger, message string) {
	writeAPIError(logger, w, http.StatusBadRequest, codeInvalidRequest, message)
}

func notFound(w http.ResponseWriter, logger *slog.Logger, message string) {
	writeAPIError(logger, w, http.StatusNotFound, codeNotFound, message)
}

func conflict(w http.ResponseWriter, logger *slog.Logger, message string) {
	writeAPIError(logger, w, http.StatusConflict, codeConflict, message)
}

func rateLimited(w http.ResponseWriter, logger *slog.Logger, retryAfterSeconds int) {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	writeAPIError(logger, w, http.StatusTooManyRequests, codeRateLimited, "too many failed attempts")
}

func requestTooLarge(w http.ResponseWriter, logger *slog.Logger) {
	writeAPIError(logger, w, http.StatusRequestEntityTooLarge, codeRequestTooLarge, "request body too large")
}

// unsupportedMediaType rejects a mutation whose body is not declared as JSON
// (spec §1.1). The CSRF header alone is not enough.
func unsupportedMediaType(w http.ResponseWriter, logger *slog.Logger) {
	writeAPIError(logger, w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "content type must be application/json")
}

// upstreamError reports a failed upstream call without echoing its cause.
func upstreamError(w http.ResponseWriter, logger *slog.Logger) {
	writeAPIError(logger, w, http.StatusBadGateway, codeUpstreamError, "upstream request failed")
}

// internalError reports an unexpected failure; the cause is logged, never sent.
func internalError(w http.ResponseWriter, logger *slog.Logger, err error) {
	if err != nil {
		logger.Error("admin request failed", "error", err)
	}
	writeAPIError(logger, w, http.StatusInternalServerError, codeInternal, "internal server error")
}
