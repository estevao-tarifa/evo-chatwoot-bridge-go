// Package evolution is a client for the Evolution GO HTTP API.
//
// It speaks the two credential modes the legacy bridge used: the global API key
// for admin routes and the per-instance token for instance routes, both carried
// in the `apikey` header (docs/contracts/evolution/auth.md). Every call runs
// under a per-call context deadline, and the underlying *http.Client (shared
// transport) is reused across calls.
//
// Upstream bodies, raw causes and private values never reach returned errors or
// logs (AGENTS.md §8, ADR-0008).
package evolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// defaultTimeout mirrors the legacy axios timeout for API calls.
	defaultTimeout = 30 * time.Second

	// Bridge-owned byte budgets, in bytes and inclusive. They are local safety
	// limits, not claims about Evolution GO server limits (ADR-0008 §4).
	maxRequestBytes  = 1 << 20  // outbound encoded JSON
	maxResponseBytes = 4 << 20  // ordinary/success response
	maxErrorBytes    = 16 << 10 // non-2xx error response

	headerAPIKey = "apikey"
)

// HTTPError carries only the numeric status of a non-2xx Evolution response.
// No upstream body, snippet or raw cause is retained: even redacted text can
// expose private values through JSON serialization or errors.As (ADR-0008 §2).
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("evolution: http status %d", e.StatusCode)
}

// upstreamError keeps cancellation/timeout classification without exposing the
// raw cause, which can include a request URL, credentials or message content.
type upstreamError struct {
	deadline  bool
	canceled  bool
	timeout   bool
	temporary bool
}

// opaqueUpstreamError reduces a transport/read cause to content-independent
// flags. The raw cause is dropped, never wrapped, so it cannot be recovered
// through errors.As or any error string.
func opaqueUpstreamError(err error) *upstreamError {
	var network interface {
		Timeout() bool
		Temporary() bool
	}
	e := &upstreamError{
		deadline: errors.Is(err, context.DeadlineExceeded),
		canceled: errors.Is(err, context.Canceled),
	}
	if errors.As(err, &network) {
		e.timeout = network.Timeout()
		e.temporary = network.Temporary()
	}
	return e
}

func (e *upstreamError) Error() string { return "upstream request failed" }

// Is preserves errors.Is(err, context.DeadlineExceeded|context.Canceled)
// without retaining the raw cause.
func (e *upstreamError) Is(target error) bool {
	return target == context.DeadlineExceeded && e.deadline ||
		target == context.Canceled && e.canceled
}

// Timeout reports a deadline or network timeout without exposing the cause.
func (e *upstreamError) Timeout() bool { return e.deadline || e.timeout }

// Temporary preserves net.Error compatibility without exposing the cause.
func (e *upstreamError) Temporary() bool { return e.temporary }

// Typed conditions callers branch on with errors.Is.
var (
	// ErrInstanceNotFound reports that /instance/all has no instance whose name
	// matches the configured InstanceName.
	ErrInstanceNotFound = errors.New("evolution: instance not found")
	// ErrInstanceTokenMissing reports that the matching instance carried no
	// token/apikey field.
	ErrInstanceTokenMissing = errors.New("evolution: instance token missing")
	// ErrNotSupported means an optional operation is unavailable. A 404 does not
	// establish that the route itself is absent (ADR-0008 §2, §3).
	ErrNotSupported = errors.New("evolution: optional operation unavailable")
	// ErrMalformedResponse is the fixed category for any undecodable upstream
	// JSON body; the raw decoder cause is never retained (ADR-0008 §1).
	ErrMalformedResponse = errors.New("evolution: malformed response")
	// ErrResponseTooLarge reports a response body that exceeded its byte budget.
	ErrResponseTooLarge = errors.New("evolution: response too large")
	// ErrRequestTooLarge reports a request body that exceeded its byte budget
	// before any network I/O.
	ErrRequestTooLarge = errors.New("evolution: request too large")
	// ErrInvalidRequest reports a request rejected locally before any I/O.
	ErrInvalidRequest = errors.New("evolution: invalid request")
	// ErrUnconfirmedSend reports that a send reached the transport but its
	// outcome could not be confirmed; it must never authorize an automatic
	// replay (ADR-0008 §5).
	ErrUnconfirmedSend = errors.New("evolution: send unconfirmed")
)

// Config configures a Client.
type Config struct {
	APIURL        string // base URL, e.g. https://evolution.example.com
	GlobalAPIKey  string // admin routes (/instance/all, /server/ok)
	InstanceName  string // matched by ResolveInstanceToken
	InstanceToken string // instance routes; optional, falls back to GlobalAPIKey

	// MarkReadPath and DownloadMediaPath are accepted for configuration
	// compatibility. An unverified custom mark-read path is refused without
	// I/O, and media download is disabled for every configuration until a
	// source-proven request contract exists (ADR-0008 §3).
	MarkReadPath      string
	DownloadMediaPath string
}

// credentialScope selects which credential a call authenticates with.
type credentialScope int

const (
	scopeAdmin credentialScope = iota
	scopeInstance
)

// Client is a reusable Evolution GO client. It is safe for concurrent use.
type Client struct {
	apiURL       string
	globalAPIKey string
	instanceName string
	markReadPath string
	httpClient   *http.Client
	logger       *slog.Logger
	timeout      time.Duration

	mu            sync.RWMutex
	instanceToken string
}

// New builds a Client. APIURL and GlobalAPIKey are required; APIURL must be an
// absolute http(s) URL without userinfo, query or fragment.
func New(cfg Config) (*Client, error) {
	if cfg.APIURL == "" {
		return nil, errors.New("evolution: APIURL is required")
	}
	base, err := normalizeAPIURL(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	if cfg.GlobalAPIKey == "" {
		return nil, errors.New("evolution: GlobalAPIKey is required")
	}
	markReadPath, err := normalizePath(cfg.MarkReadPath, defaultMarkReadPath)
	if err != nil {
		return nil, fmt.Errorf("evolution: mark read path: %w", err)
	}
	// The download path is validated for configuration compatibility but is not
	// stored: media download performs no I/O in this slice.
	if _, err := normalizePath(cfg.DownloadMediaPath, defaultDownloadMediaPath); err != nil {
		return nil, fmt.Errorf("evolution: download media path: %w", err)
	}
	return &Client{
		apiURL:        base,
		globalAPIKey:  cfg.GlobalAPIKey,
		instanceName:  cfg.InstanceName,
		instanceToken: cfg.InstanceToken,
		markReadPath:  markReadPath,
		httpClient:    &http.Client{},
		logger:        slog.Default(),
		timeout:       defaultTimeout,
	}, nil
}

// keyFor returns the apikey for the given scope. Instance routes use the
// resolved instance token; when it is absent they fall back to the global key,
// which is the legacy single-key deployment behaviour (auth.md §1, §5).
func (c *Client) keyFor(scope credentialScope) string {
	if scope == scopeAdmin {
		return c.globalAPIKey
	}
	c.mu.RLock()
	token := c.instanceToken
	c.mu.RUnlock()
	if token != "" {
		return token
	}
	return c.globalAPIKey
}

// setInstanceToken stores a token resolved from /instance/all so subsequent
// instance calls use it.
func (c *Client) setInstanceToken(token string) {
	c.mu.Lock()
	c.instanceToken = token
	c.mu.Unlock()
}

// doJSON issues exactly one JSON request. Only known request shapes are
// accepted; oversized or unknown requests are rejected before any I/O. The
// response byte budget is selected from the status, and limit+1 bytes are read
// so an oversized body is an opaque error instead of a truncated success.
func (c *Client) doJSON(ctx context.Context, method, path string, scope credentialScope, body any) (result []byte, err error) {
	var payload io.Reader
	if body != nil {
		if err := preflightRequest(body); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		if len(raw) > maxRequestBytes {
			return nil, ErrRequestTooLarge
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, payload)
	if err != nil {
		return nil, fmt.Errorf("evolution: building request: %w", opaqueUpstreamError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerAPIKey, c.keyFor(scope))

	// Enforce the no-redirect policy even with a custom client, without mutating
	// the shared client. Redirects could forward the custom apikey header.
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	start := time.Now()
	status := 0
	defer func() {
		c.logger.Info(
			"evolution call",
			"operation", operationFor(path),
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"category", errorCategory(err),
		)
	}()

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("evolution: calling api: %w", opaqueUpstreamError(err))
	}
	defer resp.Body.Close()
	status = resp.StatusCode

	limit := maxResponseBytes
	if !isSuccess(status) {
		limit = maxErrorBytes
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit+1)))
	if err != nil {
		return nil, fmt.Errorf("evolution: reading response: %w", opaqueUpstreamError(err))
	}
	if len(raw) > limit {
		return nil, ErrResponseTooLarge
	}
	if !isSuccess(status) {
		return nil, &HTTPError{StatusCode: status}
	}
	return raw, nil
}

func isSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

// HealthCheck calls the public GET /server/ok route. The legacy client used the
// admin credential even though the route is public (auth.md §2).
func (c *Client) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if _, err := c.doJSON(ctx, http.MethodGet, "/server/ok", scopeAdmin, nil); err != nil {
		return fmt.Errorf("evolution: health check: %w", err)
	}
	return nil
}

// isNotFound reports whether err is an *HTTPError with status 404.
func isNotFound(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

// preflightRequest rejects an unknown request shape or an oversized variable
// input before serialization, so an obviously huge value is not first
// duplicated unboundedly. The final encoded size is checked again after
// json.Marshal because escaping can expand a string.
func preflightRequest(body any) error {
	remaining := maxRequestBytes
	check := func(values ...string) error {
		for _, value := range values {
			remaining -= len(value)
			if remaining < 0 {
				return ErrRequestTooLarge
			}
		}
		return nil
	}
	switch b := body.(type) {
	case sendTextRequest:
		return check(b.Number, b.Text, b.ID)
	case sendMediaRequest:
		return check(b.Number, b.Type, b.URL, b.Caption, b.Filename, b.ID)
	case setWebhookRequest:
		if err := check(b.WebhookURL); err != nil {
			return err
		}
		return check(b.Subscribe...)
	case markReadRequest:
		if err := check(b.Number); err != nil {
			return err
		}
		return check(b.ID...)
	default:
		return ErrInvalidRequest
	}
}

// operationFor maps a known path to a fixed operation label for diagnostics.
func operationFor(path string) string {
	switch path {
	case "/server/ok":
		return "health"
	case "/instance/all":
		return "list_instances"
	case "/instance/status":
		return "instance_status"
	case "/user/contacts":
		return "fetch_contacts"
	case "/send/text":
		return "send_text"
	case "/send/media":
		return "send_media"
	case "/instance/connect":
		return "set_webhook"
	case defaultMarkReadPath:
		return "mark_read"
	default:
		return "unknown"
	}
}

// errorCategory maps an error to a fixed, content-independent diagnostic label.
func errorCategory(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrResponseTooLarge):
		return "oversized_response"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return "http"
	}
	return "transport"
}

// normalizeAPIURL validates an absolute http(s) base URL and trims trailing
// slashes. Userinfo, query and fragment are rejected: they would leak or garble
// the paths built from this base. The raw value is not echoed (AGENTS.md §8).
func normalizeAPIURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("evolution: APIURL is not a valid url")
	}
	isHTTP := u.Scheme == "http" || u.Scheme == "https"
	if !isHTTP || u.Host == "" {
		return "", errors.New("evolution: APIURL must be an absolute http(s) url")
	}
	if u.User != nil || strings.ContainsAny(raw, "?#") {
		return "", errors.New("evolution: APIURL must not contain userinfo, query or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// normalizePath adds a leading slash or defaults an empty value. Invalid paths
// are rejected without echoing their potentially private contents.
func normalizePath(raw, fallback string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return fallback, nil
	}
	if strings.ContainsAny(path, "?#") {
		return "", errors.New("path must not contain query or fragment")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if _, err := url.Parse(path); err != nil {
		return "", errors.New("invalid url path")
	}
	return path, nil
}
