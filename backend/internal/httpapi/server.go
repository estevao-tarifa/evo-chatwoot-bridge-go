// Package httpapi exposes the bridge HTTP surface: ops probes, the meta
// endpoint and (in later phases) the REST API consumed by the SPA.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	pathpkg "path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
)

const (
	serviceName = "evo-chatwoot-bridge"

	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second

	readinessTimeout = 2 * time.Second

	// maxBodyBytes is the global ceiling applied by the body-limit middleware:
	// the largest per-route cap. ADR-0006 §8 sets per-route caps of 16 MiB for
	// webhook envelopes (media/base64 may be inlined) and 256 KiB for
	// admin/login JSON. Routes with the tighter cap apply http.MaxBytesReader
	// themselves when they land; the ceiling exists to bound memory.
	maxBodyBytes = 16 << 20 // 16 MiB

	// adminBodyBytes caps the /api/v1/* JSON body (ADR-0006 §8: 256 KiB).
	adminBodyBytes = 256 << 10

	correlationHeader = "X-Correlation-ID"
)

// ReadinessFunc reports whether a dependency (a DB ping, later) is healthy.
// A nil ReadinessFunc means dependencies are not wired yet.
type ReadinessFunc func(ctx context.Context) error

// Options configures the HTTP server.
type Options struct {
	Addr    string // e.g. ":3000"
	Logger  *slog.Logger
	Ready   ReadinessFunc
	Version string
	Commit  string
	// Webhook is the Evolution ingress handler registered at
	// POST /api/webhooks/evolution/{connectionID}/{token}. A nil handler leaves
	// the route unregistered (dev without a database).
	Webhook http.Handler
	// ChatwootWebhook is the Chatwoot ingress handler registered at
	// POST /api/webhooks/chatwoot/{connectionID}/{token}. A nil handler leaves
	// the route unregistered.
	ChatwootWebhook http.Handler
	// Admin wires the Phase 5 admin surface (auth, stores, upstream factory).
	Admin *AdminDeps
	// CORSAllowedOrigins is the exact-origin allowlist (ADR-0001 §6).
	CORSAllowedOrigins []string
}

// NewServer builds the HTTP server with explicit timeouts and the middleware
// chain: correlation id, access log, panic recovery, body limit.
func NewServer(opts Options) *http.Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opts.Logger = logger
	return &http.Server{
		Addr:              opts.Addr,
		Handler:           newHandler(opts),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

type server struct {
	logger  *slog.Logger
	ready   ReadinessFunc
	version string
	commit  string
	admin   *AdminDeps
}

func newHandler(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	commit := opts.Commit
	if commit == "" {
		commit = "none"
	}
	s := &server{
		logger:  logger,
		ready:   opts.Ready,
		version: version,
		commit:  commit,
		admin:   opts.Admin,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	registerRoutes(mux, s)
	if opts.Webhook != nil {
		// Register the tokenless form explicitly so it reaches the handler and
		// fails closed (401) instead of falling through to a 307 that adds a
		// trailing slash (MEDIUM-2). The multi-segment wildcard matches an
		// absent/empty token too, so a missing token is rejected with 401 rather
		// than falling through to 404.
		mux.Handle("POST /api/webhooks/evolution/{connectionID}", opts.Webhook)
		mux.Handle("POST /api/webhooks/evolution/{connectionID}/{token...}", opts.Webhook)
	}
	if opts.ChatwootWebhook != nil {
		// Same tokenless-form guard as Evolution (spec §1.1): an absent token
		// fails closed 401, not a 307 redirect.
		mux.Handle("POST /api/webhooks/chatwoot/{connectionID}", opts.ChatwootWebhook)
		mux.Handle("POST /api/webhooks/chatwoot/{connectionID}/{token...}", opts.ChatwootWebhook)
	}

	// Order: correlation → access log → CORS → recovery → body limit → admin
	// body cap → webhook path guard → routes. CORS sits outside the mux so a
	// preflight OPTIONS is answered before routing. Recovery sits inside the
	// access log so a panic is still logged with the final 500 status.
	var handler http.Handler = mux
	handler = webhookPathGuard(handler)
	handler = apiBodyCap(logger)(handler)
	handler = bodyLimit(logger)(handler)
	handler = recoverPanic(logger)(handler)
	handler = cors(opts.CORSAllowedOrigins)(handler)
	handler = accessLog(logger)(handler)
	handler = correlationID(handler)
	return handler
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(s.logger, w, http.StatusOK, statusResponse{Status: "ok"})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.ready == nil {
		writeJSON(s.logger, w, http.StatusServiceUnavailable, statusResponse{
			Status: "unavailable",
			Reason: "database not wired",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		// Details stay in the logs; the probe reports a generic reason.
		s.logger.Warn("readiness check failed", "error", err)
		writeJSON(s.logger, w, http.StatusServiceUnavailable, statusResponse{
			Status: "unavailable",
			Reason: "dependency check failed",
		})
		return
	}
	writeJSON(s.logger, w, http.StatusOK, statusResponse{Status: "ok"})
}

func (s *server) meta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(s.logger, w, http.StatusOK, metaResponse{
		Name:    serviceName,
		Version: s.version,
		Commit:  s.commit,
	})
}

// correlationID propagates or generates the request correlation id.
func correlationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := observability.SanitizeCorrelationID(r.Header.Get(correlationHeader))
		if id == "" {
			id = observability.NewCorrelationID()
		}
		w.Header().Set(correlationHeader, id)
		next.ServeHTTP(w, r.WithContext(observability.WithCorrelationID(r.Context(), id)))
	})
}

// webhookPathAnchor is the fixed prefix of the webhook surface. The anchor
// match is case-insensitive and position-independent so a malformed request
// cannot smuggle the path token past the masker.
const webhookPathAnchor = "/api/webhooks/"

// isKnownWebhookSystem reports whether s is a system segment the bridge serves.
// Only a recognised system is echoed in a sanitized path; any other value could
// be a bearer token smuggled into the system slot, so it is masked too. The
// check fails safe: an unknown or future system is masked, never logged.
func isKnownWebhookSystem(s string) bool {
	switch s {
	case "evolution", "chatwoot":
		return true
	default:
		return false
	}
}

// webhookAnchorIndex returns the byte index of the FIRST case-insensitive
// occurrence of the webhook surface anchor in path, or -1 when the path is not
// on that surface. The index is valid for the ORIGINAL path: asciiLower is
// byte-length preserving, unlike strings.ToLower, which can grow a string when
// a non-ASCII rune lowercases to a longer UTF-8 sequence (U+023A and U+023E
// each lower to a 3-byte rune). An index computed on a grown string slices the
// original at the wrong offset — leaking the token, or panicking once the
// shifted index runs past the end.
//
// asciiLower is the single source of truth. An exact-match fast path must not
// be used unconditionally: the first EXACT occurrence can be LATER than the
// first case-insensitive one (e.g. `/API/webhooks/.../TOKEN/api/webhooks/...`),
// which would anchor the mask after an earlier token and log it verbatim.
func webhookAnchorIndex(path string) int {
	return strings.Index(asciiLower(path), webhookPathAnchor)
}

// asciiLower maps ASCII 'A'-'Z' to 'a'-'z' and leaves every other byte,
// including all multi-byte UTF-8 sequences, untouched. It is byte-length
// preserving, so an index into the result is valid in the input.
func asciiLower(s string) string {
	b := []byte(s)
	for i := range b {
		if 'A' <= b[i] && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// sanitizePath masks the webhook path so the bearer token never reaches a log,
// error or metric label (ADR-0006 Amendment 2, CRITICAL). Everything after the
// system segment is masked, so a token placed in the connection-id or the
// system slot is masked too; a tokenless path is not special-cased.
//
// The anchor is matched case-insensitively and wherever it appears, and the
// remainder is split into segments rather than sliced by a fixed index, so
// `//`, `/./`, `/../`, case-varied and prefixed paths are masked too (e.g.
// `//api/webhooks/evolution/1/TOKEN`). The system segment is echoed only when
// it is a known system; anything else is masked. A path off the webhook
// surface is returned unchanged.
func sanitizePath(path string) string {
	i := webhookAnchorIndex(path)
	if i < 0 {
		return path
	}
	end := i + len(webhookPathAnchor)
	if end > len(path) { // defensive: the anchor index is always in range
		return path
	}
	anchor := path[i:end]
	system, _, hasRest := strings.Cut(path[end:], "/")
	if !isKnownWebhookSystem(system) {
		return path[:i] + anchor + "***"
	}
	if !hasRest {
		return path // only the system segment; nothing after it to mask
	}
	return path[:i] + anchor + system + "/***"
}

// webhookPathGuard rejects unclean webhook paths before the ServeMux can issue
// a 307 redirect whose Location header would carry the path token (ADR-0006
// Amendment 2, CRITICAL). ServeMux cleans `//`, `/./` and `/../` and redirects
// to the cleaned path, putting the raw token in Location; this guard answers
// 404 first for any webhook-surface path that is not already clean. A single
// trailing slash is tolerated so the tokenless /api/webhooks/evolution/{id}/
// form still reaches the handler and fails closed.
func webhookPathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if webhookAnchorIndex(r.URL.Path) >= 0 && uncleanPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// uncleanPath reports whether path differs from its cleaned form by more than a
// single tolerated trailing slash (duplicate slashes, or `.`/`..` segments).
func uncleanPath(path string) bool {
	cleaned := pathpkg.Clean(path)
	return path != cleaned && path != cleaned+"/"
}

// accessLog logs one structured line per request. It never logs headers,
// cookies, bodies or query strings (AGENTS.md §8).
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			log := observability.FromContext(r.Context(), logger).With(
				"method", r.Method,
				"path", sanitizePath(r.URL.Path), // the query string is never logged: it may carry secrets
				"status", status,
				"bytes", loggedBytes(r.Method, rec.bytes),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_addr", remoteAddr(r),
			)
			switch {
			case status >= http.StatusInternalServerError:
				log.Error("request completed")
			case status >= http.StatusBadRequest:
				log.Warn("request completed")
			default:
				log.Info("request completed")
			}
		})
	}
}

// recoverPanic converts handler panics into 500 responses and logs the panic
// with its stack. The panic value reaches the logs but never the client.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { // the sentinel is meant to abort silently
					panic(rec)
				}
				log := observability.FromContext(r.Context(), logger)
				log.Error("panic recovered", "panic", rec, "stack", string(debug.Stack()))
				writeJSON(log, w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// bodyLimit rejects oversized declared bodies early and caps everything else
// while it is read. Handlers see *http.MaxBytesError from reads past the cap.
func bodyLimit(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBodyBytes {
				writeJSON(logger, w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
				return
			}
			// MaxBytesReader needs the raw writer: its requestTooLarge hook
			// (connection close on mid-read overflow) is unexported and hidden
			// by the access-log statusRecorder. Responses still go through the
			// wrapped writer so they are logged.
			raw := w
			if rec, ok := w.(*statusRecorder); ok {
				raw = rec.ResponseWriter
			}
			r.Body = http.MaxBytesReader(raw, r.Body, maxBodyBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// apiBodyCap caps the /api/v1/* JSON body at 256 KiB (ADR-0006 §8). A declared
// body over the cap is rejected before reading; a chunked body fails while
// reading, and decodeJSON maps that *http.MaxBytesError to 413.
func apiBodyCap(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				next.ServeHTTP(w, r)
				return
			}
			if r.ContentLength > adminBodyBytes {
				requestTooLarge(w, logger)
				return
			}
			raw := w
			if rec, ok := w.(*statusRecorder); ok {
				raw = rec.ResponseWriter
			}
			r.Body = http.MaxBytesReader(raw, r.Body, adminBodyBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// loggedBytes reports the bytes actually sent on the wire. For HEAD, net/http
// suppresses the body while the handler still writes it, so the recorder counts
// bytes that were never transmitted.
func loggedBytes(method string, written int) int {
	if method == http.MethodHead {
		return 0
	}
	return written
}

// statusRecorder captures the response status and size for access logs.
// ponytail: no http.Flusher/Hijacker passthrough — JSON-only API; add when
// streaming ever lands.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(logger *slog.Logger, w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The response is already committed; logging is the only handling.
		logger.Warn("failed to write json response", "error", err)
	}
}

type statusResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type metaResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}
