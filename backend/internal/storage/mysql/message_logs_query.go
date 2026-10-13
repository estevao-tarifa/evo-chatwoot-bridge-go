package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// logColumns is the read projection for message_logs. rawPayload is deliberately
// excluded: it is never read (spec §1.5).
const logColumns = `id, connectionId, direction, messageType, status,
	COALESCE(sourceMessageId, ''), COALESCE(destinationMessageId, ''),
	COALESCE(contactPhone, ''), COALESCE(contactName, ''),
	COALESCE(contentPreview, ''), COALESCE(errorMessage, ''), createdAt, processedAt`

// MessageLogsQuery is the MySQL adapter for admin.LogsStore. It is distinct
// from MessageLogs (the write adapter) and borrows the shared *sql.DB pool.
type MessageLogsQuery struct {
	db *sql.DB
}

// MessageLogsQuery implements the admin.LogsStore port.
var _ admin.LogsStore = (*MessageLogsQuery)(nil)

// NewMessageLogsQuery returns a MessageLogsQuery backed by pool. A nil pool is a
// programming error and panics.
func NewMessageLogsQuery(pool *sql.DB) *MessageLogsQuery {
	if pool == nil {
		panic("mysql: NewMessageLogsQuery requires a non-nil *sql.DB pool")
	}
	return &MessageLogsQuery{db: pool}
}

// List returns a page of message_logs and the matching total. The filter is
// applied to both the page and the count.
func (r *MessageLogsQuery) List(ctx context.Context, q admin.LogQuery) ([]admin.LogRow, int, error) {
	where, args := logFilter(q)

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM message_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count message logs: %w", err)
	}

	pageArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+logColumns+" FROM message_logs"+where+" ORDER BY createdAt DESC, id DESC LIMIT ? OFFSET ?",
		pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list message logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := []admin.LogRow{}
	for rows.Next() {
		row, err := scanLogRow(rows)
		if err != nil {
			return nil, 0, err
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate message logs: %w", err)
	}
	return logs, total, nil
}

// recentLogs returns the most recent message_logs rows for the dashboard.
func (r *MessageLogsQuery) recentLogs(ctx context.Context, limit int) ([]admin.LogRow, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+logColumns+" FROM message_logs ORDER BY createdAt DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("list recent logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := []admin.LogRow{}
	for rows.Next() {
		row, err := scanLogRow(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent logs: %w", err)
	}
	return logs, nil
}

// logFilter builds the WHERE clause and its args.
func logFilter(q admin.LogQuery) (string, []any) {
	conditions := []string{}
	args := []any{}
	if q.ConnectionID > 0 {
		conditions = append(conditions, "connectionId = ?")
		args = append(args, q.ConnectionID)
	}
	if q.Direction != "" {
		conditions = append(conditions, "direction = ?")
		args = append(args, q.Direction)
	}
	if q.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, q.Status)
	}
	if q.Search != "" {
		conditions = append(conditions,
			"(contactPhone LIKE ? OR contactName LIKE ? OR contentPreview LIKE ?)")
		like := "%" + q.Search + "%"
		args = append(args, like, like, like)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanLogRow(row rowScanner) (admin.LogRow, error) {
	var l admin.LogRow
	var processed sql.NullTime
	if err := row.Scan(
		&l.ID, &l.ConnectionID, &l.Direction, &l.MessageType, &l.Status,
		&l.SourceMessageID, &l.DestinationMessageID, &l.ContactPhone, &l.ContactName,
		&l.ContentPreview, &l.ErrorMessage, &l.CreatedAt, &processed,
	); err != nil {
		return admin.LogRow{}, fmt.Errorf("scan message log: %w", err)
	}
	if processed.Valid {
		l.ProcessedAt = &processed.Time
	}
	return l, nil
}

// utcMidnight returns today's UTC midnight for the "today messages" window.
func utcMidnight(now time.Time) time.Time {
	return now.UTC().Truncate(24 * time.Hour)
}
