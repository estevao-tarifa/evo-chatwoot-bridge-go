package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// Dashboard is the MySQL adapter for admin.DashboardStore. It borrows the shared
// *sql.DB pool and the log projection from message_logs_query.go.
type Dashboard struct {
	db    *sql.DB
	logs  *MessageLogsQuery
	clock func() time.Time
}

// Dashboard implements the admin.DashboardStore port.
var _ admin.DashboardStore = (*Dashboard)(nil)

// NewDashboard returns a Dashboard backed by pool. A nil pool is a programming
// error and panics.
func NewDashboard(pool *sql.DB) *Dashboard {
	if pool == nil {
		panic("mysql: NewDashboard requires a non-nil *sql.DB pool")
	}
	return &Dashboard{db: pool, logs: NewMessageLogsQuery(pool), clock: time.Now}
}

// Stats returns the dashboard summary. A query error is never a zero summary.
func (r *Dashboard) Stats(ctx context.Context) (admin.Stats, error) {
	var stats admin.Stats
	scalars := []struct {
		query string
		args  []any
		dst   any
	}{
		{"SELECT COUNT(*) FROM connections", nil, &stats.TotalConnections},
		{"SELECT COUNT(*) FROM connections WHERE isActive = true", nil, &stats.ActiveConnections},
		{"SELECT COUNT(*) FROM message_logs", nil, &stats.TotalMessages},
		{"SELECT COUNT(*) FROM message_logs WHERE status = 'failed'", nil, &stats.FailedMessages},
		{"SELECT COUNT(*) FROM message_logs WHERE createdAt >= ?", []any{utcMidnight(r.clock())}, &stats.TodayMessages},
	}
	for _, s := range scalars {
		if err := r.db.QueryRowContext(ctx, s.query, s.args...).Scan(s.dst); err != nil {
			return admin.Stats{}, fmt.Errorf("dashboard stat: %w", err)
		}
	}
	recent, err := r.logs.recentLogs(ctx, 10)
	if err != nil {
		return admin.Stats{}, err
	}
	stats.RecentLogs = recent
	return stats, nil
}

// MessagesPerHour returns the per-hour buckets of the last 24 hours.
func (r *Dashboard) MessagesPerHour(ctx context.Context) ([]admin.Bucket, error) {
	since := r.clock().Add(-24 * time.Hour)
	const query = `SELECT DATE_FORMAT(createdAt, '%Y-%m-%d %H:00:00') AS hour,
		SUM(CASE WHEN direction = 'whatsapp_to_chatwoot' THEN 1 ELSE 0 END),
		SUM(CASE WHEN direction = 'chatwoot_to_whatsapp' THEN 1 ELSE 0 END),
		COUNT(*)
		FROM message_logs WHERE createdAt >= ?
		GROUP BY hour ORDER BY hour ASC`
	return r.queryBuckets(ctx, query, since)
}

// MessagesPerDay returns the per-day buckets of the last 30 days.
func (r *Dashboard) MessagesPerDay(ctx context.Context) ([]admin.Bucket, error) {
	since := r.clock().Add(-30 * 24 * time.Hour)
	const query = `SELECT DATE(createdAt) AS day,
		SUM(CASE WHEN direction = 'whatsapp_to_chatwoot' THEN 1 ELSE 0 END),
		SUM(CASE WHEN direction = 'chatwoot_to_whatsapp' THEN 1 ELSE 0 END),
		COUNT(*)
		FROM message_logs WHERE createdAt >= ?
		GROUP BY day ORDER BY day ASC`
	return r.queryBuckets(ctx, query, since)
}

func (r *Dashboard) queryBuckets(ctx context.Context, query string, since time.Time) ([]admin.Bucket, error) {
	rows, err := r.db.QueryContext(ctx, query, since)
	if err != nil {
		return nil, fmt.Errorf("dashboard buckets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	buckets := []admin.Bucket{}
	for rows.Next() {
		var b admin.Bucket
		var label sql.NullString
		if err := rows.Scan(&label, &b.Incoming, &b.Outgoing, &b.Total); err != nil {
			return nil, fmt.Errorf("scan dashboard bucket: %w", err)
		}
		b.Label = label.String
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard buckets: %w", err)
	}
	return buckets, nil
}

// MessageTypes returns the message-type tallies, most frequent first.
func (r *Dashboard) MessageTypes(ctx context.Context) ([]admin.TypeCount, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT messageType, COUNT(*) AS count FROM message_logs GROUP BY messageType ORDER BY count DESC`)
	if err != nil {
		return nil, fmt.Errorf("dashboard message types: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := []admin.TypeCount{}
	for rows.Next() {
		var c admin.TypeCount
		if err := rows.Scan(&c.Type, &c.Count); err != nil {
			return nil, fmt.Errorf("scan message type: %w", err)
		}
		counts = append(counts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message types: %w", err)
	}
	return counts, nil
}
