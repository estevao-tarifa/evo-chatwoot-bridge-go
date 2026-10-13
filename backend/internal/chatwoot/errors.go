package chatwoot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrorKind classifies a failed Chatwoot call. A status alone cannot establish
// whether a mutating call took effect.
type ErrorKind int

const (
	ErrorKindUnknown ErrorKind = iota
	// ErrorKindTransport is a network or context error (timeout, DNS, reset).
	ErrorKindTransport
	// ErrorKindMalformed is a 2xx response whose body did not match the contract.
	ErrorKindMalformed
	ErrorKindUnauthorized // 401
	ErrorKindForbidden    // 403
	ErrorKindNotFound     // 404
	ErrorKindValidation   // 400, 422
	ErrorKindConflict     // 409
	ErrorKindRateLimited  // 408, 429
	ErrorKindServer       // 5xx
)

// String returns the category used in logs.
func (k ErrorKind) String() string {
	switch k {
	case ErrorKindTransport:
		return "transport"
	case ErrorKindMalformed:
		return "malformed_response"
	case ErrorKindUnauthorized:
		return "unauthorized"
	case ErrorKindForbidden:
		return "forbidden"
	case ErrorKindNotFound:
		return "not_found"
	case ErrorKindValidation:
		return "validation"
	case ErrorKindConflict:
		return "conflict"
	case ErrorKindRateLimited:
		return "rate_limited"
	case ErrorKindServer:
		return "server"
	default:
		return "unknown"
	}
}

// APIError is returned for any non-2xx response, transport failure or malformed
// body. It exposes a fixed category, a safe numeric status and the controlled
// request method/path only. Untrusted upstream bodies, decoder errors and
// transport causes (which can carry the request URL/query or private message
// text) are never reachable through Error(), formatting, JSON or Unwrap/As.
type APIError struct {
	Kind       ErrorKind
	StatusCode int
	Method     string
	Path       string
	cause      error // opaque: context.Canceled, context.DeadlineExceeded or nil
}

func (e *APIError) Error() string {
	switch e.Kind {
	case ErrorKindTransport, ErrorKindMalformed:
		return fmt.Sprintf("chatwoot: %s %s: %s", e.Method, e.Path, e.Kind.String())
	default:
		return fmt.Sprintf("chatwoot: %s %s: http %d", e.Method, e.Path, e.StatusCode)
	}
}

// Unwrap exposes only an opaque cancellation/deadline sentinel so
// errors.Is(err, context.Canceled) and errors.Is(err, context.DeadlineExceeded)
// keep working. The raw transport/decoder cause is deliberately discarded: a
// *url.Error would leak the request URL/query and a JSON decoder error could
// carry upstream text.
func (e *APIError) Unwrap() error { return e.cause }

// opaqueCause reduces a transport or read failure to a safe sentinel. It keeps
// cancellation/deadline classification while discarding any cause that could
// carry the request URL/query (e.g. *url.Error) or upstream text.
func opaqueCause(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return nil
	}
}

// Retryable reports only safe-read retry eligibility; it never permits replay
// of an attempted mutation, even on a transport failure or 5xx.
func (e *APIError) Retryable() bool {
	if e.Method != http.MethodGet {
		return false
	}
	switch e.Kind {
	case ErrorKindTransport, ErrorKindServer, ErrorKindRateLimited:
		return true
	default:
		return false
	}
}

func newAPIError(method, path string, status int) *APIError {
	return &APIError{
		Kind:       kindForStatus(status),
		StatusCode: status,
		Method:     method,
		Path:       path,
	}
}

func kindForStatus(status int) ErrorKind {
	switch {
	case status == http.StatusUnauthorized:
		return ErrorKindUnauthorized
	case status == http.StatusForbidden:
		return ErrorKindForbidden
	case status == http.StatusNotFound:
		return ErrorKindNotFound
	case status == http.StatusConflict:
		return ErrorKindConflict
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return ErrorKindRateLimited
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return ErrorKindValidation
	case status >= http.StatusInternalServerError:
		return ErrorKindServer
	default:
		return ErrorKindUnknown
	}
}

// IsNotFound reports whether err is a 404 APIError.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Kind == ErrorKindNotFound
}

// IsRetryable reports safe-read retry eligibility, never write replay safety.
func IsRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable()
}
