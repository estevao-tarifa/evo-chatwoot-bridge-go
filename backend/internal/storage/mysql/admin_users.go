package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// AdminUsers is the MySQL adapter for auth.Store over the legacy admin_users
// table (ADR-0002: schema preserved, no new column). It borrows the shared
// *sql.DB pool owned by this package.
type AdminUsers struct {
	db *sql.DB
}

// AdminUsers implements the auth.Store port.
var _ auth.Store = (*AdminUsers)(nil)

// NewAdminUsers returns an AdminUsers backed by pool. A nil pool is a
// programming error and panics.
func NewAdminUsers(pool *sql.DB) *AdminUsers {
	if pool == nil {
		panic("mysql: NewAdminUsers requires a non-nil *sql.DB pool")
	}
	return &AdminUsers{db: pool}
}

const adminUserColumns = `id, username, passwordHash, COALESCE(displayName, ''), isActive`

// Count returns the number of admin users (first-run setup gate).
func (r *AdminUsers) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count admin users: %w", err)
	}
	return count, nil
}

// FindByUsername returns the admin with username, or (nil, nil) when absent.
func (r *AdminUsers) FindByUsername(ctx context.Context, username string) (*auth.Admin, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+adminUserColumns+` FROM admin_users WHERE username = ? LIMIT 1`, username)
	return scanAdmin(row)
}

// FindByID returns the admin with id, or (nil, nil) when absent. It is the
// per-request session lookup (one indexed read, ADR-0006 §1.4).
func (r *AdminUsers) FindByID(ctx context.Context, id int) (*auth.Admin, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+adminUserColumns+` FROM admin_users WHERE id = ? LIMIT 1`, id)
	return scanAdmin(row)
}

// Create inserts an admin and returns the stored row. The admin_users table
// enforces UNIQUE(username), so a concurrent first-run Setup loses the race with
// a duplicate-key error instead of creating a second admin; it is mapped to
// auth.ErrConflict (a clean 409) rather than a raw 500.
func (r *AdminUsers) Create(ctx context.Context, username, passwordHash, displayName string) (*auth.Admin, error) {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_users (username, passwordHash, displayName, isActive) VALUES (?, ?, ?, true)`,
		username, passwordHash, nullText(displayName))
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, auth.ErrConflict
		}
		return nil, fmt.Errorf("insert admin user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("admin user id: %w", err)
	}
	return r.FindByID(ctx, int(id))
}

// isDuplicateEntry reports MySQL ER_DUP_ENTRY (1062).
func isDuplicateEntry(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// TouchLastLogin records a successful login. A failure here must not deny the
// login, so the caller logs and continues.
func (r *AdminUsers) TouchLastLogin(ctx context.Context, id int) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE admin_users SET lastLoginAt = NOW() WHERE id = ?`, id); err != nil {
		return fmt.Errorf("touch admin last login: %w", err)
	}
	return nil
}

// scanAdmin decodes one admin row, mapping sql.ErrNoRows to (nil, nil).
func scanAdmin(row *sql.Row) (*auth.Admin, error) {
	var a auth.Admin
	err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.DisplayName, &a.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan admin user: %w", err)
	}
	return &a, nil
}
