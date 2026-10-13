package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/chatwoot"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/evolution"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/storage/mysql"
)

// connectionClients carries one connection's upstream credentials. They come
// from a connections row (legacy schema), not from process env: the bridge is
// multi-connection.
type connectionClients struct {
	Chatwoot  chatwoot.Config
	Evolution evolution.Config
}

// bridgeRuntime is the per-connection composition. The Evolution port is
// injected into bridge.Deps (the Chatwoot→WhatsApp send path, E11) and also
// retained here for the test/ops surface.
type bridgeRuntime struct {
	engine    *bridge.Engine
	evolution bridge.Evolution
}

// wireConnection is the composition root for one bridge connection: it
// constructs the concrete Chatwoot/Evolution clients and the MySQL storage
// adapters and injects them into bridge.Engine as its consumer-side ports
// (ARCHITECTURE §2 rule 1). It performs no I/O beyond construction, so it is
// safe to call at startup.
func wireConnection(clients connectionClients, pool *sql.DB, logger *slog.Logger) (*bridgeRuntime, error) {
	cwClient, err := chatwoot.New(clients.Chatwoot, chatwoot.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("chatwoot client: %w", err)
	}
	evoClient, err := evolution.New(clients.Evolution)
	if err != nil {
		return nil, fmt.Errorf("evolution client: %w", err)
	}

	engine := bridge.New(bridge.Deps{
		Mappings:      mysql.NewContactMappings(pool),
		Chatwoot:      chatwoot.NewBridgeAdapter(cwClient),
		Echo:          mysql.NewEchoStore(pool),
		Dedup:         mysql.NewDedupAdapter(pool),
		Logs:          mysql.NewMessageLogs(pool),
		Evolution:     evolution.NewBridgeAdapter(evoClient),
		Conversations: mysql.NewContactMappings(pool),
		Logger:        logger,
	})
	return &bridgeRuntime{engine: engine, evolution: evolution.NewBridgeAdapter(evoClient)}, nil
}
