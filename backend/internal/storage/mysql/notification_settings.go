package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

const notificationSettingsColumns = `id, enabled, failureThreshold, cooldownMinutes,
	whatsappEnabled, COALESCE(whatsappConnectionId, 0), COALESCE(whatsappAlertPhone, ''),
	emailEnabled, COALESCE(smtpHost, ''), COALESCE(smtpPort, 0), COALESCE(smtpUser, ''),
	COALESCE(smtpPass, ''), smtpSecure, COALESCE(emailFrom, ''), COALESCE(emailTo, '')`

// Notifications is the MySQL adapter for admin.NotificationsStore over
// notification_settings and notification_alerts. It borrows the shared *sql.DB
// pool.
type Notifications struct {
	db *sql.DB
}

// Notifications implements the admin.NotificationsStore port.
var _ admin.NotificationsStore = (*Notifications)(nil)

// NewNotifications returns a Notifications backed by pool. A nil pool is a
// programming error and panics.
func NewNotifications(pool *sql.DB) *Notifications {
	if pool == nil {
		panic("mysql: NewNotifications requires a non-nil *sql.DB pool")
	}
	return &Notifications{db: pool}
}

// GetSettings returns the single settings row, or (nil, nil) when none exists.
func (r *Notifications) GetSettings(ctx context.Context) (*admin.NotificationSettings, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+notificationSettingsColumns+" FROM notification_settings ORDER BY id LIMIT 1")
	var n admin.NotificationSettings
	err := row.Scan(
		&n.ID, &n.Enabled, &n.FailureThreshold, &n.CooldownMinutes, &n.WhatsappEnabled,
		&n.WhatsappConnectionID, &n.WhatsappAlertPhone, &n.EmailEnabled, &n.SMTPHost,
		&n.SMTPPort, &n.SMTPUser, &n.SMTPPass, &n.SMTPSecure, &n.EmailFrom, &n.EmailTo,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read notification settings: %w", err)
	}
	n.HasSMTPPass = n.SMTPPass != ""
	return &n, nil
}

// UpdateSettings applies a partial change to the single settings row, creating
// it when absent. values is keyed by admin.NotificationColumns; any other key is
// rejected. smtpPass is write-only at the HTTP layer.
func (r *Notifications) UpdateSettings(ctx context.Context, values map[string]any) (*admin.NotificationSettings, error) {
	id, err := r.ensureSettingsRow(ctx)
	if err != nil {
		return nil, err
	}
	if len(values) > 0 {
		fields := make([]string, 0, len(values))
		args := make([]any, 0, len(values)+1)
		for key, value := range values {
			column, ok := admin.NotificationColumns[key]
			if !ok {
				return nil, fmt.Errorf("mysql: unknown notification field %q", key)
			}
			fields = append(fields, "`"+column+"` = ?")
			args = append(args, value)
		}
		args = append(args, id)
		query := "UPDATE notification_settings SET " + strings.Join(fields, ", ") + " WHERE id = ?"
		if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
			return nil, fmt.Errorf("update notification settings: %w", err)
		}
	}
	return r.GetSettings(ctx)
}

// ensureSettingsRow returns the single row id, inserting a default row when the
// table is empty.
func (r *Notifications) ensureSettingsRow(ctx context.Context) (int, error) {
	var id int
	err := r.db.QueryRowContext(ctx, "SELECT id FROM notification_settings ORDER BY id LIMIT 1").Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read notification settings id: %w", err)
	}
	result, err := r.db.ExecContext(ctx, "INSERT INTO notification_settings () VALUES ()")
	if err != nil {
		return 0, fmt.Errorf("insert default notification settings: %w", err)
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("notification settings id: %w", err)
	}
	return int(newID), nil
}
