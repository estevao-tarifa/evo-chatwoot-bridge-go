package mysql

import (
	"context"
	"fmt"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

const alertColumns = `id, connectionId, channel, alertType, message, success,
	COALESCE(errorMessage, ''), failureCount, createdAt`

// ListAlerts returns a page of notification_alerts and the matching total,
// most recent first.
func (r *Notifications) ListAlerts(ctx context.Context, q admin.AlertQuery) ([]admin.Alert, int, error) {
	where := ""
	args := []any{}
	if q.ConnectionID > 0 {
		where = " WHERE connectionId = ?"
		args = append(args, q.ConnectionID)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_alerts"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count notification alerts: %w", err)
	}

	pageArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+alertColumns+" FROM notification_alerts"+where+" ORDER BY createdAt DESC, id DESC LIMIT ? OFFSET ?",
		pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list notification alerts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	alerts := []admin.Alert{}
	for rows.Next() {
		var a admin.Alert
		if err := rows.Scan(&a.ID, &a.ConnectionID, &a.Channel, &a.AlertType, &a.Message,
			&a.Success, &a.ErrorMessage, &a.FailureCount, &a.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan notification alert: %w", err)
		}
		alerts = append(alerts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate notification alerts: %w", err)
	}
	return alerts, total, nil
}
