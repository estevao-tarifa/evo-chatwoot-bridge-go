package mysql

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/admin"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"

	mysqldriver "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

// adminPool opens the dedicated integration database through the production
// pool contract (parseTime, UTC), which the timestamp columns require.
func adminPool(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("TEST_MYSQL_DSN is not a valid go-sql-driver DSN: %v", err)
	}
	if !strings.HasSuffix(cfg.DBName, "_test") {
		t.Fatalf("TEST_MYSQL_DSN must point at a dedicated *_test database, got %q", cfg.DBName)
	}
	pool, err := Open(mysqlURL(cfg))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.PingContext(t.Context()); err != nil {
		t.Fatalf("test database unreachable: %v", err)
	}
	return pool, dsn
}

// adminFixture resets the disposable schema, applies the migrations and returns
// the production pool plus a connections repo.
func adminFixture(t *testing.T) (*AdminConnections, *sql.DB, string) {
	t.Helper()
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return NewAdminConnections(db), db, dsn
}

// TestConnectionsCRUD exercises create/get/list/update/deactivate and proves a
// deactivate preserves history and never hard-deletes.
func TestConnectionsCRUD(t *testing.T) {
	repo, db, _ := adminFixture(t)
	ctx := t.Context()

	created, err := repo.Create(ctx, admin.ConnectionCreate{
		Name: "Synthetic", EvolutionAPIURL: "https://evo.example", EvolutionAPIKey: "evo-key",
		EvolutionInstanceName: "instance-1", ChatwootAPIURL: "https://cw.example",
		ChatwootAPIToken: "cw-token", ChatwootAccountID: 1, ChatwootInboxID: 10, IgnoreGroups: true,
	})
	if err != nil || created == nil {
		t.Fatalf("Create() = %v, %v", created, err)
	}
	if created.ID <= 0 || !created.IsActive {
		t.Errorf("created = %+v", created)
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil || got == nil {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	if got.EvolutionAPIKey != "evo-key" || got.ChatwootAPIToken != "cw-token" {
		t.Errorf("secrets did not round-trip: %+v", got)
	}

	updated, err := repo.Update(ctx, created.ID, map[string]any{"name": "Renamed", "evolutionInstanceToken": "evo-token"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Name != "Renamed" || updated.EvolutionInstanceToken != "evo-token" {
		t.Errorf("updated = %+v", updated)
	}
	if updated.EvolutionAPIKey != "evo-key" {
		t.Error("Update() cleared an untouched secret")
	}

	if _, err := repo.Update(ctx, created.ID, map[string]any{"bogus": 1}); err == nil {
		t.Error("Update() accepted an unknown field")
	}
	if _, err := repo.Update(ctx, 9999, map[string]any{"name": "x"}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("Update(absent) err = %v, want ErrNotFound", err)
	}

	// History rows that must survive a deactivate.
	if _, err := db.ExecContext(ctx, `INSERT INTO contacts_map (connectionId, whatsappPhone, chatwootContactId) VALUES (?, ?, ?)`,
		created.ID, "5511900000001", 7); err != nil {
		t.Fatalf("insert contacts_map: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO message_logs (connectionId, direction, messageType, status) VALUES (?, ?, ?, ?)`,
		created.ID, "whatsapp_to_chatwoot", "text", "sent"); err != nil {
		t.Fatalf("insert message_logs: %v", err)
	}

	if err := repo.Deactivate(ctx, created.ID); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	after, err := repo.Get(ctx, created.ID)
	if err != nil || after == nil {
		t.Fatalf("Get() after deactivate = %v, %v", after, err)
	}
	if after.IsActive {
		t.Error("Deactivate() left isActive=true")
	}
	var contacts, logs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts_map WHERE connectionId = ?`, created.ID).Scan(&contacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_logs WHERE connectionId = ?`, created.ID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if contacts != 1 || logs != 1 {
		t.Errorf("deactivate destroyed history: contacts=%d logs=%d", contacts, logs)
	}
	if err := repo.Deactivate(ctx, 9999); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("Deactivate(absent) err = %v, want ErrNotFound", err)
	}
}

// TestAdminUsersBootstrapAndRevocation proves first-run setup, login and
// session revocation against the real admin_users table.
func TestAdminUsersBootstrapAndRevocation(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	ctx := t.Context()
	store := NewAdminUsers(db)
	svc := auth.New(store, auth.Config{Secret: "synthetic-session-secret"})

	count, err := store.Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("Count() = %d, %v, want 0", count, err)
	}
	state, err := svc.State(ctx)
	if err != nil || state != "needs_setup" {
		t.Fatalf("State() = %q, %v", state, err)
	}

	sess, token, err := svc.Setup(ctx, "admin", "secret1", "Admin")
	if err != nil || sess == nil {
		t.Fatalf("Setup() = %v, %v", sess, err)
	}
	if _, _, err := svc.Setup(ctx, "second", "secret1", ""); !errors.Is(err, auth.ErrSetupClosed) {
		t.Errorf("second Setup() err = %v, want ErrSetupClosed", err)
	}

	// The stored hash must be bcrypt cost 12.
	admin, err := store.FindByUsername(ctx, "admin")
	if err != nil || admin == nil {
		t.Fatalf("FindByUsername() = %v, %v", admin, err)
	}
	if cost, err := bcrypt.Cost([]byte(admin.PasswordHash)); err != nil || cost != auth.BcryptCost {
		t.Errorf("stored hash cost = %d, %v, want %d", cost, err, auth.BcryptCost)
	}

	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	svc.Logout(token)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("Authenticate() after logout err = %v, want ErrNoSession", err)
	}

	if _, _, _, err := svc.Login(ctx, "10.0.0.1", "admin", "secret1"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if err := store.TouchLastLogin(ctx, admin.ID); err != nil {
		t.Errorf("TouchLastLogin() error = %v", err)
	}
	var lastLogin any
	if err := db.QueryRowContext(ctx, `SELECT lastLoginAt FROM admin_users WHERE id = ?`, admin.ID).Scan(&lastLogin); err != nil {
		t.Fatalf("read lastLoginAt: %v", err)
	}
	if lastLogin == nil {
		t.Error("lastLoginAt was not recorded")
	}
}

// TestAdminUsersDuplicateConflict proves the UNIQUE(username) violation a
// concurrent first-run Setup can hit is mapped to auth.ErrConflict (a clean 409
// at the HTTP layer), not a raw driver error.
func TestAdminUsersDuplicateConflict(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	ctx := t.Context()
	store := NewAdminUsers(db)

	if _, err := store.Create(ctx, "admin", "hash", "Admin"); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if _, err := store.Create(ctx, "admin", "hash", "Admin"); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("duplicate Create() err = %v, want ErrConflict", err)
	}
}

// TestLogsPaginationSQL proves the SQL pagination, filters and total count.
func TestLogsPaginationSQL(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	ctx := t.Context()

	const insert = `INSERT INTO message_logs (connectionId, direction, messageType, status, contactPhone, contactName, contentPreview)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	for i := range 5 {
		if _, err := db.ExecContext(ctx, insert, 1, "whatsapp_to_chatwoot", "text", "sent", "5511900000001", "Ana", "hello "+string(rune('a'+i))); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, insert, 2, "chatwoot_to_whatsapp", "image", "failed", "5511900000002", "Bob", "pic"); err != nil {
		t.Fatalf("insert log: %v", err)
	}

	repo := NewMessageLogsQuery(db)

	page, total, err := repo.List(ctx, admin.LogQuery{Limit: 2, Offset: 0})
	if err != nil || total != 6 || len(page) != 2 {
		t.Fatalf("List() = %d rows, total %d, err %v; want 2 rows, total 6", len(page), total, err)
	}

	filtered, total, err := repo.List(ctx, admin.LogQuery{ConnectionID: 1, Status: "sent", Limit: 50})
	if err != nil || total != 5 || len(filtered) != 5 {
		t.Errorf("filtered List() = %d rows, total %d, err %v; want 5", len(filtered), total, err)
	}

	searched, total, err := repo.List(ctx, admin.LogQuery{Search: "Bob", Limit: 50})
	if err != nil || total != 1 || len(searched) != 1 {
		t.Errorf("search List() = %d rows, total %d, err %v; want 1", len(searched), total, err)
	}

	second, _, err := repo.List(ctx, admin.LogQuery{Limit: 2, Offset: 4})
	if err != nil || len(second) != 2 {
		t.Errorf("second page = %d rows, err %v; want 2", len(second), err)
	}
}

// TestDashboardAggregates proves the dashboard SQL against seeded rows.
func TestDashboardAggregates(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, `INSERT INTO connections (name, evolutionApiUrl, evolutionApiKey, evolutionInstanceName, chatwootApiUrl, chatwootApiToken, chatwootAccountId, isActive) VALUES ('a','https://e','k','i','https://c','t',1,true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO connections (name, evolutionApiUrl, evolutionApiKey, evolutionInstanceName, chatwootApiUrl, chatwootApiToken, chatwootAccountId, isActive) VALUES ('b','https://e','k','i','https://c','t',1,false)`); err != nil {
		t.Fatal(err)
	}

	const insert = `INSERT INTO message_logs (connectionId, direction, messageType, status, createdAt) VALUES (?, ?, ?, ?, ?)`
	rows := []struct {
		direction, msgType, status string
		createdAt                  time.Time
	}{
		{"whatsapp_to_chatwoot", "text", "sent", time.Now().UTC()},
		{"whatsapp_to_chatwoot", "image", "failed", time.Now().UTC()},
		{"chatwoot_to_whatsapp", "text", "sent", time.Now().UTC().Add(-2 * time.Hour)},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, insert, 1, r.direction, r.msgType, r.status, r.createdAt); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}

	dash := NewDashboard(db)
	stats, err := dash.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.TotalConnections != 2 || stats.ActiveConnections != 1 {
		t.Errorf("connection stats = %+v", stats)
	}
	if stats.TotalMessages != 3 || stats.FailedMessages != 1 || stats.TodayMessages != 3 {
		t.Errorf("message stats = %+v", stats)
	}
	if len(stats.RecentLogs) != 3 {
		t.Errorf("recent logs = %d, want 3", len(stats.RecentLogs))
	}

	hours, err := dash.MessagesPerHour(ctx)
	if err != nil {
		t.Fatalf("MessagesPerHour() error = %v", err)
	}
	if sumTotals(hours) != 3 {
		t.Errorf("per-hour total = %d, want 3", sumTotals(hours))
	}

	days, err := dash.MessagesPerDay(ctx)
	if err != nil {
		t.Fatalf("MessagesPerDay() error = %v", err)
	}
	if sumTotals(days) != 3 {
		t.Errorf("per-day total = %d, want 3", sumTotals(days))
	}

	types, err := dash.MessageTypes(ctx)
	if err != nil {
		t.Fatalf("MessageTypes() error = %v", err)
	}
	if len(types) != 2 {
		t.Errorf("message types = %+v, want 2", types)
	}
}

func sumTotals(buckets []admin.Bucket) int64 {
	var sum int64
	for _, b := range buckets {
		sum += b.Total
	}
	return sum
}

// TestNotificationSettingsWriteOnly proves the settings row is created on first
// write and that smtpPass is only replaced, never cleared.
func TestNotificationSettingsWriteOnly(t *testing.T) {
	db, dsn := adminPool(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	ctx := t.Context()
	repo := NewNotifications(db)

	settings, err := repo.GetSettings(ctx)
	if err != nil || settings != nil {
		t.Fatalf("GetSettings() = %v, %v, want nil", settings, err)
	}

	updated, err := repo.UpdateSettings(ctx, map[string]any{"enabled": true, "smtpPass": "synthetic-pass", "emailTo": "ops@example.test"})
	if err != nil || updated == nil {
		t.Fatalf("UpdateSettings() = %v, %v", updated, err)
	}
	if !updated.Enabled || !updated.HasSMTPPass || updated.SMTPPass != "synthetic-pass" {
		t.Errorf("settings = %+v", updated)
	}

	// A later update that omits smtpPass leaves it unchanged.
	again, err := repo.UpdateSettings(ctx, map[string]any{"enabled": false})
	if err != nil || again == nil {
		t.Fatalf("second UpdateSettings() = %v, %v", again, err)
	}
	if again.Enabled || again.SMTPPass != "synthetic-pass" {
		t.Errorf("smtpPass was not preserved: %+v", again)
	}

	if _, err := repo.UpdateSettings(ctx, map[string]any{"bogus": 1}); err == nil {
		t.Error("UpdateSettings() accepted an unknown field")
	}
}
