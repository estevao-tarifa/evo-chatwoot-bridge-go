package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// bridgeURLKey is the legacy bridge_settings key (settings.ts getBridgeUrl).
const bridgeURLKey = "BRIDGE_URL"

// BridgeSettings is the MySQL adapter for admin.SettingsStore over the baseline
// bridge_settings KV table. A legacy install that never created the table is not
// an error: BridgeURL returns "" (spec §6.5 R4 — no migration).
type BridgeSettings struct {
	db *sql.DB
}

// NewBridgeSettings returns a BridgeSettings backed by pool. A nil pool is a
// programming error and panics.
func NewBridgeSettings(pool *sql.DB) *BridgeSettings {
	if pool == nil {
		panic("mysql: NewBridgeSettings requires a non-nil *sql.DB pool")
	}
	return &BridgeSettings{db: pool}
}

// BridgeURL returns the stored BRIDGE_URL, or "" when unset or when the table
// does not exist.
func (r *BridgeSettings) BridgeURL(ctx context.Context) (string, error) {
	var value sql.NullString
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM bridge_settings WHERE `key` = ? LIMIT 1", bridgeURLKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if isMissingTable(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read bridge url setting: %w", err)
	}
	return value.String, nil
}

// isMissingTable reports MySQL ER_NO_SUCH_TABLE (1146).
func isMissingTable(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1146
}
