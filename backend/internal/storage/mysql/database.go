// Package mysql owns the MySQL 8 data layer: the database/sql pool
// (database.go) and the forward-only migration runner (migrate.go), per
// ADR-0002. Query helpers built on top are added by later slices.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" driver for database/sql
)

const (
	maxOpenConns    = 25
	maxIdleConns    = 10
	connMaxLifetime = 5 * time.Minute
	connMaxIdleTime = 1 * time.Minute

	pingTimeout = 3 * time.Second
)

// Open parses a legacy mysql:// DATABASE_URL and returns a configured
// database/sql pool. The driver is lazy: Open does not contact the server, so
// the process can start while MySQL is still booting. Readiness is reported by
// Ping (the /readyz probe).
func Open(rawURL string) (*sql.DB, error) {
	cfg, err := ConfigFromURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("database configuration: %w", err)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("opening mysql pool: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	return db, nil
}

// Ping reports whether the database is reachable, bounded by a short
// timeout so a hung server cannot stall the readiness probe.
func Ping(ctx context.Context, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("pinging mysql: %w", err)
	}
	return nil
}
