package main

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/chatwoot"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/evolution"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/httpapi"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/storage/mysql"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/worker"
)

// Compile-time proof the concrete types satisfy the consumer-side ports.
var (
	_ httpapi.ConnectionRegistry = (*registry)(nil)
	_ httpapi.IngressStore       = (*mysql.DedupAdapter)(nil)
	_ httpapi.Enqueuer           = (*worker.Queue)(nil)
	_ httpapi.IngressEngine      = (*bridge.Engine)(nil)
)

// connRuntime is the per-connection ingress runtime. engine is nil for an
// inactive connection or when wiring failed (the handler fails closed).
type connRuntime struct {
	conn   bridge.Connection
	engine *bridge.Engine
}

// registry is the boot-built per-connection runtime table.
type registry struct {
	mu   sync.RWMutex
	byID map[int]connRuntime
}

// Lookup implements httpapi.ConnectionRegistry. An inactive connection returns
// ok=true with a nil engine so the handler answers 403; an unknown connection
// returns ok=false (404). A nil engine is returned untyped so the interface is
// nil (never a typed-nil *bridge.Engine).
func (r *registry) Lookup(connectionID int) (bridge.Connection, httpapi.IngressEngine, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.byID[connectionID]
	if !ok {
		return bridge.Connection{}, nil, false
	}
	if rt.engine == nil {
		return rt.conn, nil, true
	}
	return rt.conn, rt.engine, true
}

// anyEngine returns one wired engine (any will do: boot reconciliation only
// touches the shared dedup repository), or nil when none is wired.
func (r *registry) anyEngine() *bridge.Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rt := range r.byID {
		if rt.engine != nil {
			return rt.engine
		}
	}
	return nil
}

// buildRegistry loads every connection and composes the active ones. A wiring
// failure is logged and leaves the connection registered but unwired, so a
// single bad row cannot abort boot.
func buildRegistry(ctx context.Context, pool *sql.DB, logger *slog.Logger) (*registry, error) {
	connections, err := mysql.NewConnections(pool).List(ctx)
	if err != nil {
		return nil, err
	}
	reg := &registry{byID: make(map[int]connRuntime, len(connections))}
	for _, c := range connections {
		rt := connRuntime{conn: bridge.Connection{
			ID:              c.ID,
			ChatwootInboxID: c.ChatwootInboxID,
			IsActive:        c.IsActive,
			IgnoreGroups:    c.IgnoreGroups,
		}}
		if c.IsActive {
			built, err := wireConnection(connectionClients{
				Chatwoot: chatwoot.Config{
					APIURL:    c.ChatwootAPIURL,
					APIToken:  c.ChatwootAPIToken,
					AccountID: c.ChatwootAccountID,
				},
				Evolution: evolution.Config{
					APIURL:        c.EvolutionAPIURL,
					GlobalAPIKey:  c.EvolutionAPIKey,
					InstanceName:  c.EvolutionInstanceName,
					InstanceToken: c.EvolutionInstanceToken,
				},
			}, pool, logger)
			if err != nil {
				logger.Error("connection wiring failed", "connection_id", c.ID, "error", err)
			} else {
				rt.engine = built.engine
			}
		}
		reg.byID[c.ID] = rt
	}
	return reg, nil
}
