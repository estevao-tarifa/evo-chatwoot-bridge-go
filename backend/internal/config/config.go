// Package config loads and validates the bridge configuration from
// environment variables.
//
// Empty variables are treated as unset. Values that are set but invalid make
// Load fail fast so the process never starts half-configured.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort     = 3000
	defaultLogLevel = "info"

	// defaultAppEnv is the secure default: production posture, so the webhook
	// token secret is required unless APP_ENV is explicitly "development"
	// (ADR-0006 Amendment 2).
	defaultAppEnv = "production"

	// Worker defaults (ADR-0005): bounded queue, fixed pool, graceful drain.
	defaultQueueCapacity = 1024
	defaultQueueMaxBytes = 64 << 20 // 64 MiB
	defaultWorkers       = 8
	defaultDrainTimeout  = 30 * time.Second
	defaultStaleAge      = 15 * time.Minute

	// minStaleAge mirrors the repository's independent 15-minute floor.
	minStaleAge = 15 * time.Minute
)

// validLogLevels mirrors what observability.ParseLevel accepts.
var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// Config is the validated runtime configuration.
type Config struct {
	Port                  int
	DatabaseURL           string
	LogLevel              string
	CORSAllowedOrigins    []string
	BridgeURL             string
	AdminUser             string
	AdminPass             string
	AppEnv                string
	WebhookTokenSecret    string
	ChatwootWebhookSecret string
	SessionSecret         string
	CookieSecure          bool
	CookieSameSite        string
	QueueCapacity         int
	QueueMaxBytes         int64
	Workers               int
	DrainTimeout          time.Duration
	StaleAge              time.Duration
}

// Load reads and validates the configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		Port:               defaultPort,
		LogLevel:           defaultLogLevel,
		CORSAllowedOrigins: []string{},
		AppEnv:             defaultAppEnv,
		QueueCapacity:      defaultQueueCapacity,
		QueueMaxBytes:      defaultQueueMaxBytes,
		Workers:            defaultWorkers,
		DrainTimeout:       defaultDrainTimeout,
		StaleAge:           defaultStaleAge,
	}

	if raw := getenv("PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("config: PORT %q is not a valid tcp port", raw)
		}
		cfg.Port = port
	}

	// Legacy deployments set a mysql:// URL (drizzle/mysql2). The storage
	// layer converts it to a go-sql-driver DSN; keep the env format stable.
	cfg.DatabaseURL = getenv("DATABASE_URL")
	if cfg.DatabaseURL != "" {
		if err := validateDatabaseURL(cfg.DatabaseURL); err != nil {
			return Config{}, err
		}
	}

	if raw := getenv("LOG_LEVEL"); raw != "" {
		cfg.LogLevel = strings.ToLower(raw)
	}
	if !validLogLevels[cfg.LogLevel] {
		return Config{}, fmt.Errorf("config: LOG_LEVEL %q must be one of debug, info, warn, error", cfg.LogLevel)
	}

	origins, err := parseOrigins(getenv("CORS_ALLOWED_ORIGINS"))
	if err != nil {
		return Config{}, err
	}
	cfg.CORSAllowedOrigins = origins

	if raw := getenv("BRIDGE_URL"); raw != "" {
		bridgeURL, err := normalizeBaseURL(raw)
		if err != nil {
			return Config{}, fmt.Errorf("config: BRIDGE_URL %w", err)
		}
		cfg.BridgeURL = bridgeURL
	}

	// Bootstrap pair for the admin user (ADR-0006): both or neither.
	cfg.AdminUser = getenv("ADMIN_USER")
	cfg.AdminPass = getenv("ADMIN_PASS")
	if (cfg.AdminUser == "") != (cfg.AdminPass == "") {
		return Config{}, errors.New("config: ADMIN_USER and ADMIN_PASS must be set together")
	}

	// Deployment environment. Production is the default posture: the webhook
	// token secret is required unless APP_ENV is explicitly "development", so
	// an unset APP_ENV fails fast rather than silently starting without webhook
	// authentication (ADR-0006 Amendment 2).
	if raw := getenv("APP_ENV"); raw != "" {
		cfg.AppEnv = strings.ToLower(raw)
	}
	switch cfg.AppEnv {
	case "development", "production":
	default:
		return Config{}, fmt.Errorf("config: APP_ENV %q must be development or production", cfg.AppEnv)
	}

	// Secrets: never logged, never echoed in an error message. The requirement
	// is enforced at the end of Load, after the other fields are validated.
	cfg.WebhookTokenSecret = getenv("WEBHOOK_TOKEN_SECRET")
	cfg.ChatwootWebhookSecret = getenv("CHATWOOT_WEBHOOK_SECRET")
	cfg.SessionSecret = getenv("SESSION_SECRET")

	// Cookie posture (ADR-0006 §1.4, ADR-0007 §5): Secure defaults to true in
	// production and explicitly to false in development (local HTTP); an explicit
	// COOKIE_SECURE overrides either. SameSite=None is only valid with Secure —
	// browsers reject the pair otherwise — so that combination fails fast rather
	// than silently locking the admin out.
	switch cfg.AppEnv {
	case "development":
		cfg.CookieSecure = false // explicit dev default: local HTTP
	default:
		cfg.CookieSecure = true // production posture
	}
	if raw := getenv("COOKIE_SECURE"); raw != "" {
		secure, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("config: COOKIE_SECURE %q must be a boolean", raw)
		}
		cfg.CookieSecure = secure
	}
	cfg.CookieSameSite = "lax"
	if raw := getenv("COOKIE_SAMESITE"); raw != "" {
		cfg.CookieSameSite = strings.ToLower(raw)
	}
	switch cfg.CookieSameSite {
	case "lax", "strict", "none":
	default:
		return Config{}, fmt.Errorf("config: COOKIE_SAMESITE %q must be lax, strict or none", cfg.CookieSameSite)
	}
	if cfg.CookieSameSite == "none" && !cfg.CookieSecure {
		return Config{}, errors.New("config: COOKIE_SAMESITE=none requires COOKIE_SECURE=true")
	}

	if err := parsePositiveInt("QUEUE_CAPACITY", getenv("QUEUE_CAPACITY"), &cfg.QueueCapacity); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt64("QUEUE_MAX_BYTES", getenv("QUEUE_MAX_BYTES"), &cfg.QueueMaxBytes); err != nil {
		return Config{}, err
	}
	if err := parsePositiveInt("WORKERS", getenv("WORKERS"), &cfg.Workers); err != nil {
		return Config{}, err
	}
	if err := parseDuration("DRAIN_TIMEOUT", getenv("DRAIN_TIMEOUT"), &cfg.DrainTimeout); err != nil {
		return Config{}, err
	}
	if err := parseDuration("STALE_AGE", getenv("STALE_AGE"), &cfg.StaleAge); err != nil {
		return Config{}, err
	}
	if cfg.StaleAge < minStaleAge {
		return Config{}, fmt.Errorf("config: STALE_AGE %s is below the %s minimum", cfg.StaleAge, minStaleAge)
	}

	// Production posture: the webhook token secret is required unless APP_ENV is
	// explicitly "development". Checked last so a misconfigured secret never
	// masks an earlier field error.
	if cfg.AppEnv != "development" && cfg.WebhookTokenSecret == "" {
		return Config{}, errors.New("config: WEBHOOK_TOKEN_SECRET is required unless APP_ENV is development")
	}
	// The admin session secret is required in production (ADR-0006 §1.4); an
	// empty secret would make every session invalid.
	if cfg.AppEnv != "development" && cfg.SessionSecret == "" {
		return Config{}, errors.New("config: SESSION_SECRET is required unless APP_ENV is development")
	}

	return cfg, nil
}

// parsePositiveInt overrides target when raw is set and positive.
func parsePositiveInt(name, raw string, target *int) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fmt.Errorf("config: %s %q must be a positive integer", name, raw)
	}
	*target = value
	return nil
}

// parsePositiveInt64 overrides target when raw is set and positive.
func parsePositiveInt64(name, raw string, target *int64) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("config: %s %q must be a positive integer", name, raw)
	}
	*target = value
	return nil
}

// parseDuration overrides target when raw is set and a positive duration.
func parseDuration(name, raw string, target *time.Duration) error {
	if raw == "" {
		return nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fmt.Errorf("config: %s %q must be a positive duration (e.g. 30s)", name, raw)
	}
	*target = value
	return nil
}

func getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// redactURL renders raw for error messages and logs with userinfo and
// secret-bearing query values replaced, so credentials never reach stderr
// (AGENTS.md §8).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	q := u.Query()
	for key := range q {
		if secretParam(key) {
			q.Set(key, "redacted")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// secretParam reports whether a query parameter name may carry a secret.
func secretParam(key string) bool {
	k := strings.ToLower(key)
	for _, marker := range []string{"pass", "pwd", "secret", "token", "key", "auth", "credential"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

// parseOrigins splits the comma-separated CORS allowlist. Wildcards are
// rejected: every origin must be exact (ADR-0001).
func parseOrigins(raw string) ([]string, error) {
	origins := []string{}
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		if err := validateOrigin(origin); err != nil {
			return nil, err
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

func validateOrigin(origin string) error {
	if origin == "*" {
		return errors.New(`config: CORS_ALLOWED_ORIGINS must list exact origins, "*" is not allowed`)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("config: CORS_ALLOWED_ORIGINS entry %q is not a valid origin", redactURL(origin))
	}
	isHTTP := u.Scheme == "http" || u.Scheme == "https"
	hasExtras := u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil
	if !isHTTP || u.Host == "" || hasExtras {
		return fmt.Errorf("config: CORS_ALLOWED_ORIGINS entry %q must be scheme://host[:port] with no path", redactURL(origin))
	}
	if strings.Contains(u.Host, "*") {
		return fmt.Errorf("config: CORS_ALLOWED_ORIGINS entry %q must be an exact origin, wildcards are not allowed", redactURL(origin))
	}
	return nil
}

// validateDatabaseURL requires a mysql:// URL with a host. A prefix-only check
// let "mysql://" (no host) through. The message is redacted so a malformed URL
// never echoes credentials (AGENTS.md §8).
func validateDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mysql" || u.Host == "" {
		return fmt.Errorf("config: DATABASE_URL %q must be a mysql:// url with a host", redactURL(raw))
	}
	return nil
}

// normalizeBaseURL validates an absolute http(s) URL and trims trailing
// slashes (legacy did the same before building webhook URLs). Userinfo, query
// and fragment are rejected: they would leak or garble the upstream webhook
// URLs built from this base. Messages are redacted (AGENTS.md §8).
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse echoes the raw string in its error; redact it instead.
		return "", fmt.Errorf("%q is not a valid url", redactURL(raw))
	}
	isHTTP := u.Scheme == "http" || u.Scheme == "https"
	if !isHTTP || u.Host == "" {
		return "", fmt.Errorf("%q must be an absolute http(s) url", redactURL(raw))
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q must not contain userinfo, query or fragment", redactURL(raw))
	}
	return strings.TrimRight(raw, "/"), nil
}
