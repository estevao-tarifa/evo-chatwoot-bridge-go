package mysql

import "testing"

// TestSlice4_ConnectionsList proves the connections repo reads every row with
// the bridge-owned fields the registry needs (integration, gated by DSN).
func TestSlice4_ConnectionsList(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	const insert = `INSERT INTO connections
		(name, evolutionApiUrl, evolutionApiKey, evolutionInstanceName,
		 chatwootApiUrl, chatwootApiToken, chatwootAccountId, chatwootInboxId, isActive, ignoreGroups)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := db.ExecContext(t.Context(), insert,
		"Synthetic Active", "https://evo.example", "evo-key", "instance-1",
		"https://cw.example", "cw-token", 1, 10, true, true); err != nil {
		t.Fatalf("insert active connection: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), insert,
		"Synthetic Inactive", "https://evo2.example", "evo-key-2", "instance-2",
		"https://cw2.example", "cw-token-2", 2, 20, false, false); err != nil {
		t.Fatalf("insert inactive connection: %v", err)
	}

	connections, err := NewConnections(db).List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(connections) != 2 {
		t.Fatalf("List() len = %d, want 2", len(connections))
	}

	first := connections[0]
	if first.ID != 1 || first.Name != "Synthetic Active" || !first.IsActive || !first.IgnoreGroups {
		t.Errorf("first connection = %+v", first)
	}
	if first.ChatwootInboxID != 10 || first.ChatwootAccountID != 1 || first.EvolutionInstanceName != "instance-1" {
		t.Errorf("first connection fields = %+v", first)
	}
	if connections[1].IsActive {
		t.Errorf("second connection IsActive = true, want false")
	}
}
