package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// allEnvKeys is every variable Load reads; tests set all of them per case so
// subtests never inherit state from each other or the host environment.
var allEnvKeys = []string{
	"PORT",
	"DATABASE_URL",
	"LOG_LEVEL",
	"CORS_ALLOWED_ORIGINS",
	"BRIDGE_URL",
	"ADMIN_USER",
	"ADMIN_PASS",
	"APP_ENV",
	"WEBHOOK_TOKEN_SECRET",
	"CHATWOOT_WEBHOOK_SECRET",
	"SESSION_SECRET",
	"COOKIE_SECURE",
	"COOKIE_SAMESITE",
	"QUEUE_CAPACITY",
	"QUEUE_MAX_BYTES",
	"WORKERS",
	"DRAIN_TIMEOUT",
	"STALE_AGE",
}

// defaultConfig is Load()'s result with only the defaults applied for the given
// app env; tests embed it so a new field does not silently break every case.
func defaultConfig(appEnv string) Config {
	return Config{
		Port:               defaultPort,
		LogLevel:           defaultLogLevel,
		CORSAllowedOrigins: []string{},
		AppEnv:             appEnv,
		CookieSecure:       appEnv != "development",
		CookieSameSite:     "lax",
		QueueCapacity:      defaultQueueCapacity,
		QueueMaxBytes:      defaultQueueMaxBytes,
		Workers:            defaultWorkers,
		DrainTimeout:       defaultDrainTimeout,
		StaleAge:           defaultStaleAge,
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name:    "nothing set defaults to production and requires the webhook token secret",
			env:     map[string]string{},
			wantErr: "WEBHOOK_TOKEN_SECRET",
		},
		{
			name: "development defaults",
			env:  map[string]string{"APP_ENV": "development"},
			want: defaultConfig("development"),
		},
		{
			name: "full valid configuration",
			env: map[string]string{
				"PORT":                 "8080",
				"DATABASE_URL":         "mysql://bridge:secret@db:3306/bridge",
				"LOG_LEVEL":            "DEBUG",
				"CORS_ALLOWED_ORIGINS": "https://admin.example, http://localhost:5173",
				"BRIDGE_URL":           "https://bridge.example/",
				"ADMIN_USER":           "admin",
				"ADMIN_PASS":           "synthetic-pass",
				"WEBHOOK_TOKEN_SECRET": "synthetic-webhook-secret",
				"SESSION_SECRET":       "synthetic-session-secret",
				"QUEUE_CAPACITY":       "256",
				"QUEUE_MAX_BYTES":      "1048576",
				"WORKERS":              "4",
				"DRAIN_TIMEOUT":        "10s",
				"STALE_AGE":            "20m",
			},
			want: Config{
				Port:               8080,
				DatabaseURL:        "mysql://bridge:secret@db:3306/bridge",
				LogLevel:           "debug",
				CORSAllowedOrigins: []string{"https://admin.example", "http://localhost:5173"},
				BridgeURL:          "https://bridge.example",
				AdminUser:          "admin",
				AdminPass:          "synthetic-pass",
				AppEnv:             defaultAppEnv,
				WebhookTokenSecret: "synthetic-webhook-secret",
				SessionSecret:      "synthetic-session-secret",
				CookieSecure:       true,
				CookieSameSite:     "lax",
				QueueCapacity:      256,
				QueueMaxBytes:      1048576,
				Workers:            4,
				DrainTimeout:       10 * time.Second,
				StaleAge:           20 * time.Minute,
			},
		},
		{
			name:    "port is not a number",
			env:     map[string]string{"PORT": "abc"},
			wantErr: "PORT",
		},
		{
			name:    "port zero is rejected",
			env:     map[string]string{"PORT": "0"},
			wantErr: "PORT",
		},
		{
			name:    "port above 65535 is rejected",
			env:     map[string]string{"PORT": "70000"},
			wantErr: "PORT",
		},
		{
			name:    "database url with a non-mysql scheme is rejected",
			env:     map[string]string{"DATABASE_URL": "postgres://user:pass@db:5432/bridge"},
			wantErr: "DATABASE_URL",
		},
		{
			name:    "database url without a host is rejected",
			env:     map[string]string{"DATABASE_URL": "mysql://"},
			wantErr: "DATABASE_URL",
		},
		{
			name:    "unknown log level is rejected",
			env:     map[string]string{"LOG_LEVEL": "verbose"},
			wantErr: "LOG_LEVEL",
		},
		{
			name:    "cors wildcard is rejected",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "*"},
			wantErr: `"*"`,
		},
		{
			name:    "cors entry without scheme is rejected",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "https://ok.example,notaurl"},
			wantErr: "must be scheme://host[:port]",
		},
		{
			name:    "cors entry with trailing slash is rejected",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "https://ok.example/"},
			wantErr: "must be scheme://host[:port]",
		},
		{
			name:    "cors wildcard host is rejected",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "https://*"},
			wantErr: "wildcards are not allowed",
		},
		{
			name:    "cors wildcard subdomain is rejected",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "https://*.example.com"},
			wantErr: "wildcards are not allowed",
		},
		{
			name: "cors empty segments are ignored",
			env: map[string]string{
				"CORS_ALLOWED_ORIGINS": ",,https://ok.example,,",
				"APP_ENV":              "development",
			},
			want: Config{
				Port:               defaultPort,
				LogLevel:           defaultLogLevel,
				CORSAllowedOrigins: []string{"https://ok.example"},
				AppEnv:             "development",
				CookieSecure:       false,
				CookieSameSite:     "lax",
				QueueCapacity:      defaultQueueCapacity,
				QueueMaxBytes:      defaultQueueMaxBytes,
				Workers:            defaultWorkers,
				DrainTimeout:       defaultDrainTimeout,
				StaleAge:           defaultStaleAge,
			},
		},
		{
			name:    "bridge url without scheme is rejected",
			env:     map[string]string{"BRIDGE_URL": "bridge.example"},
			wantErr: "BRIDGE_URL",
		},
		{
			name:    "bridge url with a non-http scheme is rejected",
			env:     map[string]string{"BRIDGE_URL": "ftp://bridge.example"},
			wantErr: "BRIDGE_URL",
		},
		{
			name:    "bridge url with userinfo is rejected",
			env:     map[string]string{"BRIDGE_URL": "https://user:pass@bridge.example"},
			wantErr: "must not contain userinfo",
		},
		{
			name:    "bridge url with query is rejected",
			env:     map[string]string{"BRIDGE_URL": "https://bridge.example/?token=x"},
			wantErr: "must not contain userinfo",
		},
		{
			name:    "bridge url with fragment is rejected",
			env:     map[string]string{"BRIDGE_URL": "https://bridge.example/#frag"},
			wantErr: "must not contain userinfo",
		},
		{
			name:    "admin user without password is rejected",
			env:     map[string]string{"ADMIN_USER": "admin"},
			wantErr: "must be set together",
		},
		{
			name:    "admin password without user is rejected",
			env:     map[string]string{"ADMIN_PASS": "synthetic-pass"},
			wantErr: "must be set together",
		},
		{
			name: "values are trimmed",
			env: map[string]string{
				"PORT":         " 9090 ",
				"DATABASE_URL": "  mysql://bridge:secret@db:3306/bridge  ",
				"APP_ENV":      "development",
			},
			want: Config{
				Port:               9090,
				DatabaseURL:        "mysql://bridge:secret@db:3306/bridge",
				LogLevel:           defaultLogLevel,
				CORSAllowedOrigins: []string{},
				AppEnv:             "development",
				CookieSecure:       false,
				CookieSameSite:     "lax",
				QueueCapacity:      defaultQueueCapacity,
				QueueMaxBytes:      defaultQueueMaxBytes,
				Workers:            defaultWorkers,
				DrainTimeout:       defaultDrainTimeout,
				StaleAge:           defaultStaleAge,
			},
		},
		{
			name:    "invalid app env is rejected",
			env:     map[string]string{"APP_ENV": "staging"},
			wantErr: "APP_ENV",
		},
		{
			name:    "production requires the webhook token secret",
			env:     map[string]string{"APP_ENV": "production"},
			wantErr: "WEBHOOK_TOKEN_SECRET",
		},
		{
			name: "production requires the session secret",
			env: map[string]string{
				"APP_ENV":              "production",
				"WEBHOOK_TOKEN_SECRET": "synthetic-webhook-secret",
			},
			wantErr: "SESSION_SECRET",
		},
		{
			name: "invalid cookie samesite is rejected",
			env: map[string]string{
				"APP_ENV":         "development",
				"COOKIE_SAMESITE": "bogus",
			},
			wantErr: "COOKIE_SAMESITE",
		},
		{
			name: "samesite none without secure is rejected",
			env: map[string]string{
				"APP_ENV":              "production",
				"WEBHOOK_TOKEN_SECRET": "synthetic-webhook-secret",
				"SESSION_SECRET":       "synthetic-session-secret",
				"COOKIE_SECURE":        "false",
				"COOKIE_SAMESITE":      "none",
			},
			wantErr: "COOKIE_SAMESITE=none requires COOKIE_SECURE=true",
		},
		{
			name: "samesite none with secure is accepted",
			env: map[string]string{
				"APP_ENV":              "production",
				"WEBHOOK_TOKEN_SECRET": "synthetic-webhook-secret",
				"SESSION_SECRET":       "synthetic-session-secret",
				"COOKIE_SAMESITE":      "none",
			},
			want: Config{
				Port:               defaultPort,
				LogLevel:           defaultLogLevel,
				CORSAllowedOrigins: []string{},
				AppEnv:             "production",
				WebhookTokenSecret: "synthetic-webhook-secret",
				SessionSecret:      "synthetic-session-secret",
				CookieSecure:       true,
				CookieSameSite:     "none",
				QueueCapacity:      defaultQueueCapacity,
				QueueMaxBytes:      defaultQueueMaxBytes,
				Workers:            defaultWorkers,
				DrainTimeout:       defaultDrainTimeout,
				StaleAge:           defaultStaleAge,
			},
		},
		{
			name:    "non-positive queue capacity is rejected",
			env:     map[string]string{"QUEUE_CAPACITY": "0"},
			wantErr: "QUEUE_CAPACITY",
		},
		{
			name:    "invalid queue byte budget is rejected",
			env:     map[string]string{"QUEUE_MAX_BYTES": "lots"},
			wantErr: "QUEUE_MAX_BYTES",
		},
		{
			name:    "invalid worker count is rejected",
			env:     map[string]string{"WORKERS": "-1"},
			wantErr: "WORKERS",
		},
		{
			name:    "invalid drain timeout is rejected",
			env:     map[string]string{"DRAIN_TIMEOUT": "soon"},
			wantErr: "DRAIN_TIMEOUT",
		},
		{
			name:    "stale age below the floor is rejected",
			env:     map[string]string{"STALE_AGE": "1m"},
			wantErr: "STALE_AGE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range allEnvKeys {
				t.Setenv(key, tt.env[key])
			}

			got, err := Load()

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %q, want error containing %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() unexpected error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLoadRedactsDatabaseURLCredentials is the regression test for the D1
// defect: no error produced by Load may echo DATABASE_URL credentials
// (AGENTS.md §8).
func TestLoadRedactsDatabaseURLCredentials(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		secrets []string
	}{
		{
			name:    "userinfo credentials",
			env:     map[string]string{"DATABASE_URL": "postgres://alice:hunter2@db:5432/bridge"},
			secrets: []string{"alice", "hunter2"},
		},
		{
			name:    "secret-bearing query parameters",
			env:     map[string]string{"DATABASE_URL": "postgres://db:5432/bridge?password=query-pass&token=query-token&sslmode=require"},
			secrets: []string{"query-pass", "query-token"},
		},
		{
			name:    "unparseable url",
			env:     map[string]string{"DATABASE_URL": "postgres://alice:hunter2@db:5432/%zz"},
			secrets: []string{"alice", "hunter2"},
		},
		{
			name: "later validation failure does not echo it",
			env: map[string]string{
				"DATABASE_URL": "mysql://alice:hunter2@db:3306/bridge",
				"LOG_LEVEL":    "verbose",
			},
			secrets: []string{"alice", "hunter2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range allEnvKeys {
				t.Setenv(key, tt.env[key])
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), "DATABASE_URL") && !strings.Contains(err.Error(), "LOG_LEVEL") {
				t.Errorf("error does not name the offending variable: %v", err)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked credential %q: %v", secret, err)
				}
			}
		})
	}
}

// TestLoadRedactsBridgeURLAndCORSCredentials proves BRIDGE_URL and CORS entry
// error messages never echo credentials (AGENTS.md §8).
func TestLoadRedactsBridgeURLAndCORSCredentials(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		secrets []string
	}{
		{
			name:    "bridge url userinfo and query",
			env:     map[string]string{"BRIDGE_URL": "https://alice:hunter2@bridge.example/?token=query-token"},
			secrets: []string{"alice", "hunter2", "query-token"},
		},
		{
			name:    "unparseable bridge url",
			env:     map[string]string{"BRIDGE_URL": "https://alice:hunter2@bridge.example/%zz"},
			secrets: []string{"alice", "hunter2"},
		},
		{
			name:    "cors entry with userinfo and path",
			env:     map[string]string{"CORS_ALLOWED_ORIGINS": "https://alice:hunter2@admin.example/path"},
			secrets: []string{"alice", "hunter2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range allEnvKeys {
				t.Setenv(key, tt.env[key])
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			for _, secret := range tt.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked credential %q: %v", secret, err)
				}
			}
		})
	}
}
