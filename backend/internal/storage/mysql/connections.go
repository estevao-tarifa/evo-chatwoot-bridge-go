package mysql

import (
	"context"
	"database/sql"
	"fmt"
)

// Connection is the bridge-owned view of one `connections` row (legacy schema).
// It carries the upstream credentials so cmd/bridge can build the per-connection
// clients; those credentials never reach a log or an error message
// (AGENTS.md §8). It is read-only in this slice.
type Connection struct {
	ID                     int
	Name                   string
	EvolutionAPIURL        string
	EvolutionAPIKey        string
	EvolutionInstanceName  string
	EvolutionInstanceToken string
	ChatwootAPIURL         string
	ChatwootAPIToken       string
	ChatwootAccountID      int
	ChatwootInboxID        int
	IsActive               bool
	IgnoreGroups           bool
}

// Connections reads the connections table. It borrows the *sql.DB pool owned by
// this package.
type Connections struct {
	db *sql.DB
}

// NewConnections returns a Connections backed by pool. A nil pool is a
// programming error and panics.
func NewConnections(pool *sql.DB) *Connections {
	if pool == nil {
		panic("mysql: NewConnections requires a non-nil *sql.DB pool")
	}
	return &Connections{db: pool}
}

// listConnectionsSQL reads every configured connection in id order. The column
// names are the legacy camelCase identifiers (ADR-0002: schema preserved).
const listConnectionsSQL = `SELECT id, name, evolutionApiUrl, evolutionApiKey,
	evolutionInstanceName, COALESCE(evolutionInstanceToken, ''),
	chatwootApiUrl, chatwootApiToken, chatwootAccountId, COALESCE(chatwootInboxId, 0),
	isActive, ignoreGroups
	FROM connections
	ORDER BY id`

// List returns every configured connection. A read error is never an empty
// list: the caller must not treat an error as "no connections".
func (r *Connections) List(ctx context.Context) ([]Connection, error) {
	rows, err := r.db.QueryContext(ctx, listConnectionsSQL)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	connections := []Connection{}
	for rows.Next() {
		var c Connection
		if err := rows.Scan(
			&c.ID, &c.Name, &c.EvolutionAPIURL, &c.EvolutionAPIKey,
			&c.EvolutionInstanceName, &c.EvolutionInstanceToken,
			&c.ChatwootAPIURL, &c.ChatwootAPIToken, &c.ChatwootAccountID, &c.ChatwootInboxID,
			&c.IsActive, &c.IgnoreGroups,
		); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		connections = append(connections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connections: %w", err)
	}
	return connections, nil
}
