package main

import (
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// TestSlice4_RegistryLookup proves the runtime registry maps unknown → 404,
// inactive → 403 (ok, nil engine) and active → a usable engine, and that a nil
// engine is returned as an untyped nil interface.
func TestSlice4_RegistryLookup(t *testing.T) {
	active := &bridge.Engine{}
	reg := &registry{byID: map[int]connRuntime{
		1: {conn: bridge.Connection{ID: 1, IsActive: false}},
		2: {conn: bridge.Connection{ID: 2, IsActive: true}, engine: active},
	}}

	if _, _, ok := reg.Lookup(99); ok {
		t.Error("unknown connection ok = true, want false")
	}

	conn, engine, ok := reg.Lookup(1)
	if !ok || conn.IsActive {
		t.Errorf("inactive Lookup = %+v, %v; want ok with inactive conn", conn, ok)
	}
	if engine != nil {
		t.Error("inactive connection returned an engine, want nil")
	}

	conn, engine, ok = reg.Lookup(2)
	if !ok || !conn.IsActive || engine == nil {
		t.Errorf("active Lookup = %+v, %v, %v; want an active conn with an engine", conn, engine, ok)
	}
	if got := reg.anyEngine(); got != active {
		t.Errorf("anyEngine() = %p, want %p", got, active)
	}
}
