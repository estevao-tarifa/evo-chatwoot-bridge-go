package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
)

// AdminConnections is the MySQL adapter for admin.ConnectionsStore over the
// legacy connections table. It is a distinct type from Connections (the
// read-only boot reader) because it exposes every admin field and the CRUD
// operations; both borrow the shared *sql.DB pool.
type AdminConnections struct {
	db *sql.DB
}

// AdminConnections implements the admin.ConnectionsStore port.
var _ admin.ConnectionsStore = (*AdminConnections)(nil)

// NewAdminConnections returns an AdminConnections backed by pool. A nil pool is
// a programming error and panics.
func NewAdminConnections(pool *sql.DB) *AdminConnections {
	if pool == nil {
		panic("mysql: NewAdminConnections requires a non-nil *sql.DB pool")
	}
	return &AdminConnections{db: pool}
}

const adminConnectionColumns = `id, name, evolutionApiUrl, evolutionApiKey,
	evolutionInstanceName, COALESCE(evolutionInstanceToken, ''),
	chatwootApiUrl, chatwootApiToken, chatwootAccountId, COALESCE(chatwootInboxId, 0),
	COALESCE(chatwootInboxName, ''), isActive, ignoreGroups, signMessages,
	autoReadMessages, rejectCalls, COALESCE(rejectCallMessage, ''),
	COALESCE(n8nWebhookUrl, ''), webhookConfigured, inboxGreetingEnabled,
	COALESCE(inboxGreetingMessage, ''), inboxAutoAssignment, inboxCsatEnabled,
	inboxWorkingHoursEnabled, COALESCE(inboxOutOfOfficeMessage, ''),
	COALESCE(inboxTimezone, ''), inboxAllowAfterResolved, inboxLockSingleConversation,
	COALESCE(inboxSenderNameType, ''), COALESCE(inboxBusinessName, ''), createdAt, updatedAt`

// List returns every connection in id order.
func (r *AdminConnections) List(ctx context.Context) ([]admin.Connection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+adminConnectionColumns+` FROM connections ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list admin connections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	connections := []admin.Connection{}
	for rows.Next() {
		c, err := scanAdminConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin connections: %w", err)
	}
	return connections, nil
}

// Get returns the connection with id, or (nil, nil) when absent.
func (r *AdminConnections) Get(ctx context.Context, id int) (*admin.Connection, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+adminConnectionColumns+` FROM connections WHERE id = ?`, id)
	c, err := scanAdminConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

const insertAdminConnectionSQL = `INSERT INTO connections
	(name, evolutionApiUrl, evolutionApiKey, evolutionInstanceName, evolutionInstanceToken,
	 chatwootApiUrl, chatwootApiToken, chatwootAccountId, chatwootInboxId,
	 ignoreGroups, signMessages, autoReadMessages, rejectCalls, rejectCallMessage, n8nWebhookUrl)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Create inserts a connection and returns the stored row.
func (r *AdminConnections) Create(ctx context.Context, in admin.ConnectionCreate) (*admin.Connection, error) {
	result, err := r.db.ExecContext(ctx, insertAdminConnectionSQL,
		in.Name, in.EvolutionAPIURL, in.EvolutionAPIKey, in.EvolutionInstanceName,
		nullText(in.EvolutionInstanceToken),
		in.ChatwootAPIURL, in.ChatwootAPIToken, in.ChatwootAccountID,
		nullableInt(in.ChatwootInboxID),
		in.IgnoreGroups, in.SignMessages, in.AutoReadMessages, in.RejectCalls,
		nullText(in.RejectCallMessage), nullText(in.N8NWebhookURL),
	)
	if err != nil {
		return nil, fmt.Errorf("insert connection: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("connection id: %w", err)
	}
	return r.Get(ctx, int(id))
}

// Update applies a partial change. values is keyed by the canonical field names
// in admin.ConnectionColumns; any other key is rejected, so a dynamic SET clause
// can never inject SQL. It returns ErrNotFound for an absent connection.
func (r *AdminConnections) Update(ctx context.Context, id int, values map[string]any) (*admin.Connection, error) {
	if len(values) == 0 {
		return r.Get(ctx, id)
	}
	fields := make([]string, 0, len(values))
	args := make([]any, 0, len(values)+1)
	for key, value := range values {
		column, ok := admin.ConnectionColumns[key]
		if !ok {
			return nil, fmt.Errorf("mysql: unknown connection field %q", key)
		}
		fields = append(fields, "`"+column+"` = ?")
		args = append(args, value)
	}
	args = append(args, id)
	query := "UPDATE connections SET " + strings.Join(fields, ", ") + " WHERE id = ?"
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("update connection: %w", err)
	}
	conn, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, admin.ErrNotFound
	}
	return conn, nil
}

// Deactivate is the conservative delete (spec §6.5 R7): it sets isActive=false
// and preserves contacts_map/message_logs history. It never hard-deletes.
func (r *AdminConnections) Deactivate(ctx context.Context, id int) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE connections SET isActive = false WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deactivate connection: %w", err)
	}
	conn, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if conn == nil {
		return admin.ErrNotFound
	}
	return nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAdminConnection(row rowScanner) (admin.Connection, error) {
	var c admin.Connection
	err := row.Scan(
		&c.ID, &c.Name, &c.EvolutionAPIURL, &c.EvolutionAPIKey, &c.EvolutionInstanceName,
		&c.EvolutionInstanceToken, &c.ChatwootAPIURL, &c.ChatwootAPIToken,
		&c.ChatwootAccountID, &c.ChatwootInboxID, &c.ChatwootInboxName, &c.IsActive,
		&c.IgnoreGroups, &c.SignMessages, &c.AutoReadMessages, &c.RejectCalls,
		&c.RejectCallMessage, &c.N8NWebhookURL, &c.WebhookConfigured,
		&c.InboxGreetingEnabled, &c.InboxGreetingMessage, &c.InboxAutoAssignment,
		&c.InboxCsatEnabled, &c.InboxWorkingHours, &c.InboxOutOfOffice,
		&c.InboxTimezone, &c.InboxAllowAfterResolve, &c.InboxLockSingle,
		&c.InboxSenderNameType, &c.InboxBusinessName, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return admin.Connection{}, sql.ErrNoRows
		}
		return admin.Connection{}, fmt.Errorf("scan connection: %w", err)
	}
	return c, nil
}

// nullableInt stores 0 as NULL for the nullable int columns.
func nullableInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}
