package main

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/chatwoot"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/evolution"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/storage/mysql"
)

// TestWireConnection proves the composition root builds a usable engine and
// Evolution port from concrete clients and the shared pool. The pool points at
// a dead address: construction is lazy and the assertions below touch no SQL.
func TestWireConnection(t *testing.T) {
	cwSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"meta":{"count":0,"current_page":1,"has_more":false},"payload":[]}`)
	}))
	defer cwSrv.Close()
	evoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"Info":{"ID":"SYNTHETIC-ID"}}}`)
	}))
	defer evoSrv.Close()

	pool := lazyPool(t)
	runtime, err := wireConnection(connectionClients{
		Chatwoot:  chatwoot.Config{APIURL: cwSrv.URL, APIToken: "synthetic", AccountID: 1},
		Evolution: evolution.Config{APIURL: evoSrv.URL, GlobalAPIKey: "synthetic"},
	}, pool, discardTestLogger())
	if err != nil {
		t.Fatalf("wireConnection() error = %v", err)
	}
	if runtime == nil || runtime.engine == nil {
		t.Fatal("wireConnection() did not build an engine")
	}
	if runtime.evolution == nil {
		t.Fatal("wireConnection() did not build the Evolution port")
	}

	sent, err := runtime.evolution.SendText(t.Context(), "5511900000001", "hi")
	if err != nil || sent == nil || sent.MessageID != "SYNTHETIC-ID" {
		t.Fatalf("Evolution SendText() = %+v, %v; want SYNTHETIC-ID", sent, err)
	}

	// The engine is usable without touching storage: an E7 broadcast is ignored
	// before any port call.
	outcome, err := runtime.engine.HandleEvent(t.Context(), bridge.Event{
		Connection: bridge.Connection{ID: 1, ChatwootInboxID: 1, IsActive: true},
		Payload:    []byte(`{"event":"Message","data":{"Info":{"Chat":"status@broadcast"}}}`),
	})
	if err != nil || outcome != bridge.OutcomeIgnored {
		t.Fatalf("HandleEvent() = %v, %v; want ignored", outcome, err)
	}
}

func TestWireConnectionInvalidConfig(t *testing.T) {
	if _, err := wireConnection(connectionClients{}, lazyPool(t), discardTestLogger()); err == nil {
		t.Fatal("wireConnection() error = nil, want a chatwoot config error")
	}
}

// lazyPool opens a *sql.DB at a dead address. sql.Open does not dial, so the
// pool is usable for construction-only tests.
func lazyPool(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := mysql.Open("mysql://bridge:bridge@127.0.0.1:1/evo_bridge")
	if err != nil {
		t.Fatalf("mysql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
