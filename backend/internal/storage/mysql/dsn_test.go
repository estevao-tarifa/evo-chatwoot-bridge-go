package mysql

import (
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestConfigFromURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
		check   func(t *testing.T, cfg *mysqldriver.Config)
	}{
		{
			name: "full url",
			raw:  "mysql://bridge:s3cr3t@db.example:3307/evo_bridge",
			check: func(t *testing.T, cfg *mysqldriver.Config) {
				if cfg.Addr != "db.example:3307" {
					t.Errorf("Addr = %q, want db.example:3307", cfg.Addr)
				}
				if cfg.DBName != "evo_bridge" {
					t.Errorf("DBName = %q, want evo_bridge", cfg.DBName)
				}
				if cfg.User != "bridge" || cfg.Passwd != "s3cr3t" {
					t.Errorf("credentials = %q/%q, want bridge/s3cr3t", cfg.User, cfg.Passwd)
				}
				if !cfg.ParseTime {
					t.Error("ParseTime = false, want true")
				}
				if cfg.Loc != time.UTC {
					t.Errorf("Loc = %v, want UTC", cfg.Loc)
				}
				if cfg.Collation != "utf8mb4_0900_ai_ci" {
					t.Errorf("Collation = %q, want utf8mb4_0900_ai_ci", cfg.Collation)
				}
				if got := cfg.Params["time_zone"]; got != "'+00:00'" {
					t.Errorf("time_zone = %q, want '+00:00'", got)
				}
				if cfg.Timeout != 5*time.Second {
					t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
				}
				if cfg.MultiStatements {
					t.Error("MultiStatements = true, want false on the runtime connection")
				}
			},
		},
		{
			name: "default port",
			raw:  "mysql://bridge@localhost/bridge",
			check: func(t *testing.T, cfg *mysqldriver.Config) {
				if cfg.Addr != "localhost:3306" {
					t.Errorf("Addr = %q, want localhost:3306", cfg.Addr)
				}
			},
		},
		{
			name: "percent-encoded password",
			raw:  "mysql://bridge:p%40ss%2Fword@127.0.0.1:3306/bridge",
			check: func(t *testing.T, cfg *mysqldriver.Config) {
				if cfg.Passwd != "p@ss/word" {
					t.Errorf("Passwd = %q, want p@ss/word", cfg.Passwd)
				}
			},
		},
		{
			name: "ipv6 host",
			raw:  "mysql://bridge@[::1]/bridge",
			check: func(t *testing.T, cfg *mysqldriver.Config) {
				if cfg.Addr != "[::1]:3306" {
					t.Errorf("Addr = %q, want [::1]:3306", cfg.Addr)
				}
			},
		},
		{
			name: "tls pass-through",
			raw:  "mysql://bridge@db.example/bridge?tls=skip-verify",
			check: func(t *testing.T, cfg *mysqldriver.Config) {
				if cfg.TLSConfig != "skip-verify" {
					t.Errorf("TLSConfig = %q, want skip-verify", cfg.TLSConfig)
				}
			},
		},
		{name: "wrong scheme", raw: "http://db.example/bridge", wantErr: "mysql://"},
		{name: "missing host", raw: "mysql:///bridge", wantErr: "missing the host"},
		{name: "missing database", raw: "mysql://db.example", wantErr: "missing the database name"},
		{name: "root path only", raw: "mysql://db.example/", wantErr: "missing the database name"},
		{name: "unsupported parameter", raw: "mysql://db.example/bridge?charset=latin1", wantErr: `"charset"`},
		{name: "multiStatements rejected", raw: "mysql://db.example/bridge?multiStatements=true", wantErr: `"multiStatements"`},
		{name: "unknown tls value", raw: "mysql://db.example/bridge?tls=require", wantErr: "tls value"},
		{name: "invalid url", raw: "mysql://%zz", wantErr: "not a valid url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ConfigFromURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ConfigFromURL(%q) error = nil, want %q", tt.raw, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ConfigFromURL(%q) error = %v, want substring %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigFromURL(%q) error = %v", tt.raw, err)
			}
			tt.check(t, cfg)
		})
	}
}

// TestConfigFromURL_NoCredentialLeak is the H1 regression: url.Parse echoes the
// raw string (including the password) in its error, so a malformed DSN must not
// surface either the raw value or the wrapped parse error (AGENTS.md §8).
func TestConfigFromURL_NoCredentialLeak(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		password string
	}{
		{
			name:     "invalid percent escape in password",
			raw:      "mysql://bridge:S3cr3t%zz@db.example/evo_bridge",
			password: "S3cr3t%zz",
		},
		{
			name:     "invalid port after password",
			raw:      "mysql://bridge:hunter2@db.example:notaport/evo_bridge",
			password: "hunter2",
		},
		{
			name:     "invalid escape only in userinfo",
			raw:      "mysql://bridge:p%2%zz@db.example/evo_bridge",
			password: "p%2%zz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConfigFromURL(tt.raw)
			if err == nil {
				t.Fatalf("ConfigFromURL(%q) error = nil, want an error", tt.raw)
			}
			if strings.Contains(err.Error(), tt.password) {
				t.Errorf("error leaks the password: %v", err)
			}
			if strings.Contains(err.Error(), tt.raw) {
				t.Errorf("error leaks the raw DSN: %v", err)
			}
		})
	}
}

// TestConfigFromURL_NoClientFoundRows guards the atomic-claim contract of
// ADR-0004 §1: clientFoundRows=true changes ON DUPLICATE KEY UPDATE affected-row
// semantics (a duplicate would report 1 = false winner), so the runtime DSN
// must never set it.
func TestConfigFromURL_NoClientFoundRows(t *testing.T) {
	cfg, err := ConfigFromURL("mysql://bridge:pw@127.0.0.1:3306/evo_bridge")
	if err != nil {
		t.Fatalf("ConfigFromURL() error = %v", err)
	}
	if cfg.ClientFoundRows {
		t.Error("ClientFoundRows = true, want false (breaks ADR-0004 atomic claim)")
	}
	if strings.Contains(cfg.FormatDSN(), "clientFoundRows=true") {
		t.Errorf("FormatDSN() = %q, must not enable clientFoundRows", cfg.FormatDSN())
	}
}

func TestConfigFromURL_FormatDSN(t *testing.T) {
	cfg, err := ConfigFromURL("mysql://bridge:pw@127.0.0.1:3306/evo_bridge")
	if err != nil {
		t.Fatalf("ConfigFromURL() error = %v", err)
	}
	dsn := cfg.FormatDSN()
	for _, want := range []string{"parseTime=true", "collation=utf8mb4_0900_ai_ci", "time_zone=%27%2B00%3A00%27"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("FormatDSN() = %q, want it to contain %q", dsn, want)
		}
	}
	if strings.Contains(dsn, "multiStatements=true") {
		t.Errorf("FormatDSN() = %q, runtime DSN must not enable multiStatements", dsn)
	}

	// FormatDSN omits loc when it is UTC (the driver default); the parsed
	// config must still be UTC.
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(FormatDSN()) error = %v", err)
	}
	if parsed.Loc != time.UTC {
		t.Errorf("round-tripped Loc = %v, want UTC", parsed.Loc)
	}
}
