// Package chatwoot is the Chatwoot REST API client used by the bridge:
// contacts, conversations, messages (JSON and multipart), inboxes and webhook
// signature verification. It implements the small consumer-side ports declared
// by the bridge domain (ARCHITECTURE.md §2).
//
// Endpoints and payloads follow docs/contracts/chatwoot/*. A transport or HTTP
// failure is always returned as an error and never collapsed into "not found"
// (G0, docs/bridge/ROUTING_MATRIX.md §1).
package chatwoot

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
	"strconv"
	"strings"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
)

const (
	// defaultTimeout matches the legacy axios client (contacts.md §1).
	defaultTimeout = 30 * time.Second
	// maxResponseBytes bounds any decoded response body.
	maxResponseBytes = 8 << 20 // 8 MiB
)

// Config holds the Chatwoot credentials the client needs.
type Config struct {
	APIURL    string // e.g. https://chatwoot.example.com (trailing slash trimmed)
	APIToken  string // api_access_token header value; never logged
	AccountID int    // account-scoped paths
}

// Client is a reusable Chatwoot API client. All methods are safe for concurrent
// use and take a per-call context.
type Client struct {
	baseURL   string // {apiUrl}/api/v1
	accountID int
	token     string
	http      *http.Client
	logger    *slog.Logger
}

// Option customises the client at construction time.
type Option func(*Client)

// WithHTTPClient uses hc's transport/timeout (tests, custom transport). New
// clones hc and disables redirects without modifying the supplied client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.http = hc
		}
	}
}

// WithLogger sets the structured logger; nil falls back to slog.Default.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) { c.logger = l }
}

// New validates cfg and returns a ready client. The base URL is
// {APIURL}/api/v1 and every request carries the api_access_token header.
func New(cfg Config, opts ...Option) (*Client, error) {
	base, err := normalizeAPIURL(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	if cfg.APIToken == "" {
		return nil, errors.New("chatwoot: api token is required")
	}
	if cfg.AccountID <= 0 {
		return nil, errors.New("chatwoot: account id must be positive")
	}

	c := &Client{
		baseURL:   base + "/api/v1",
		accountID: cfg.AccountID,
		token:     cfg.APIToken,
		http:      &http.Client{Timeout: defaultTimeout},
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	// Clone even an injected client: callers retain their redirect policy, but
	// this credentialed API client never follows redirects (no contract needs
	// them). Go otherwise forwards custom auth headers to foreign targets.
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &hc
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c, nil
}

// accountURL builds an account-scoped path, e.g. rest "contacts/7" ->
// "/accounts/42/contacts/7".
func (c *Client) accountURL(rest string) string {
	return "/accounts/" + strconv.Itoa(c.accountID) + "/" + rest
}

// doJSON sends an optional JSON payload and decodes a JSON response into out.
// out may be nil when the body is not needed.
func (c *Client) doJSON(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("chatwoot: encoding request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return errors.New("chatwoot: building request failed") // URL errors may contain query identities.
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.execute(req, out)
}

// execute performs req, maps the response to a typed error and decodes out.
// It never logs headers, query strings or bodies: the query may carry a phone
// number and the header carries the token (AGENTS.md §8).
func (c *Client) execute(req *http.Request, out any) error {
	req.Header.Set("api_access_token", c.token)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	duration := time.Since(start)
	ctx := req.Context()
	if err != nil {
		apiErr := &APIError{Kind: ErrorKindTransport, Method: req.Method, Path: req.URL.Path, cause: opaqueCause(err)}
		c.logCall(ctx, req.Method, req.URL.Path, 0, duration, apiErr)
		return apiErr
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		apiErr := &APIError{Kind: ErrorKindTransport, Method: req.Method, Path: req.URL.Path, cause: opaqueCause(readErr)}
		c.logCall(ctx, req.Method, req.URL.Path, resp.StatusCode, duration, apiErr)
		return apiErr
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		apiErr := newAPIError(req.Method, req.URL.Path, resp.StatusCode)
		c.logCall(ctx, req.Method, req.URL.Path, resp.StatusCode, duration, apiErr)
		return apiErr
	}

	// A non-nil out means the endpoint's contract requires a JSON body. A 2xx
	// with an empty body is then malformed, never a zero value: decoding nothing
	// would silently yield a "not found"/empty result and let the engine CREATE
	// an entity that already exists (G0, ROUTING_MATRIX.md §1). Bodyless calls
	// pass out == nil (UpdateContactName) and stay valid.
	if out != nil {
		if err := decodeResponse(data, out); err != nil {
			// The decoder cause is discarded: a JSON type/overflow error can
			// carry upstream text (e.g. {"secret":1e9999}).
			apiErr := &APIError{
				Kind: ErrorKindMalformed, Method: req.Method, Path: req.URL.Path,
				StatusCode: resp.StatusCode,
			}
			c.logCall(ctx, req.Method, req.URL.Path, resp.StatusCode, duration, apiErr)
			return apiErr
		}
	}
	c.logCall(ctx, req.Method, req.URL.Path, resp.StatusCode, duration, nil)
	return nil
}

// decodeResponse checks the shared JSON boundary. Entity responses require an
// id for mapping/echo suppression; list envelopes validate their own shapes.
func decodeResponse(data []byte, out any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("chatwoot: missing response body")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	var id int
	switch entity := out.(type) {
	case *Message:
		id = entity.ID
	case *Conversation:
		id = entity.ID
	case *Inbox:
		id = entity.ID
	default:
		return nil
	}
	if id <= 0 {
		return errors.New("chatwoot: response missing positive id")
	}
	return nil
}

// logCall emits one structured line per upstream call: method, path, status and
// duration, plus the error category on failure. Query strings and headers are
// deliberately excluded (AGENTS.md §8).
//
// Severity follows the method, not Retryable(): a failed mutation (method != GET)
// is an unknown-outcome write that may have taken effect and needs operator
// attention (Error), while a failed read is safe and only reported (Warn).
func (c *Client) logCall(ctx context.Context, method, path string, status int, d time.Duration, apiErr *APIError) {
	logger := observability.FromContext(ctx, c.logger)
	attrs := []any{
		"method", method,
		"path", path,
		"status", status,
		"duration_ms", d.Milliseconds(),
	}
	switch {
	case apiErr == nil:
		logger.Info("chatwoot request", attrs...)
	case method != http.MethodGet:
		logger.Error("chatwoot request failed", append(attrs, "error_category", apiErr.Kind.String())...)
	default:
		logger.Warn("chatwoot request failed", append(attrs, "error_category", apiErr.Kind.String())...)
	}
}

// normalizeAPIURL validates an absolute http(s) base URL and trims trailing
// slashes. The raw value is never echoed back: it may carry credentials.
func normalizeAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("chatwoot: api url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("chatwoot: api url is not a valid url")
	}
	isHTTP := u.Scheme == "http" || u.Scheme == "https"
	hasExtras := u.User != nil || u.RawQuery != "" || u.Fragment != ""
	if !isHTTP || u.Host == "" || hasExtras {
		return "", errors.New("chatwoot: api url must be an absolute http(s) url without userinfo, query or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}
