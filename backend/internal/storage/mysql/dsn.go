package mysql

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const defaultPort = "3306"

// ConfigFromURL converts the legacy DATABASE_URL format (mysql://..., as used
// by the Node/drizzle stack) into a go-sql-driver configuration.
//
// Guarantees applied to every connection (ADR-0002 §5):
//   - TCP transport with dial/read/write timeouts;
//   - parseTime + UTC location, and session time_zone='+00:00' so NOW() and
//     CURRENT_TIMESTAMP default expressions are UTC;
//   - utf8mb4 with the MySQL 8 default collation;
//   - multiStatements stays disabled. The migrator flips the flag on a
//     private copy; enabling it in DATABASE_URL is rejected loudly.
//
// Query parameters are rejected except `tls` (true|skip-verify|preferred):
// silently ignoring or forwarding them could produce a connection with
// different semantics than the operator intended (legacy ssl= JSON objects
// are NOT supported — configure `tls` explicitly).
func ConfigFromURL(raw string) (*mysqldriver.Config, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse echoes the raw string (including the password) in its error,
		// so neither raw nor err may be included here (AGENTS.md §8).
		return nil, fmt.Errorf("database url %s is not a valid url", redactDSN(raw))
	}
	if u.Scheme != "mysql" {
		return nil, errors.New(`database url must start with "mysql://"`)
	}
	if u.Host == "" {
		return nil, errors.New("database url is missing the host")
	}
	if u.Path == "" || u.Path == "/" {
		return nil, errors.New("database url is missing the database name")
	}

	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = defaultPort
	}

	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}

	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Collation = "utf8mb4_0900_ai_ci"
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}

	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second

	for key := range u.Query() {
		if key != "tls" {
			return nil, fmt.Errorf("database url parameter %q is not supported", key)
		}
	}
	if tls := u.Query().Get("tls"); tls != "" {
		switch tls {
		case "true", "skip-verify", "preferred":
			cfg.TLSConfig = tls
		default:
			return nil, fmt.Errorf(`database url tls value %q is not supported (want true, skip-verify or preferred)`, tls)
		}
	}

	return cfg, nil
}

// redactDSN renders raw for error messages with the userinfo replaced, so a
// malformed DATABASE_URL never leaks the password into logs (AGENTS.md §8).
// When raw cannot be parsed it falls back to the scheme plus a placeholder: a
// password can only appear after the "://" separator, so the scheme is safe.
func redactDSN(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if i := strings.Index(raw, "://"); i >= 0 {
			return raw[:i+3] + "<redacted>"
		}
		return "<redacted>"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	return u.String()
}
