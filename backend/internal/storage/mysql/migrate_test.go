package mysql

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/migrations"
)

// migrationFiles is a unit test: it only touches the embedded static files.
func TestMigrationFiles(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles() error = %v", err)
	}
	want := []string{"0001_baseline.sql", "0002_processed_messages.sql", "0003_contacts_map_index.sql", "0004_processed_messages_exact_collation.sql"}
	if !slices.Equal(files, want) {
		t.Fatalf("migrationFiles() = %v, want %v", files, want)
	}
}

// TestMigrationConfigTimeouts locks down M2: the migration connection clears the
// runtime 10s read/write timeouts (long DDL on production-sized tables) and
// enables multiStatements, while the runtime config keeps both.
func TestMigrationConfigTimeouts(t *testing.T) {
	const raw = "mysql://bridge:pw@127.0.0.1:3306/evo_bridge"

	runtimeCfg, err := ConfigFromURL(raw)
	if err != nil {
		t.Fatalf("ConfigFromURL() error = %v", err)
	}
	if runtimeCfg.ReadTimeout != 10*time.Second || runtimeCfg.WriteTimeout != 10*time.Second {
		t.Errorf("runtime timeouts = %v/%v, want 10s/10s", runtimeCfg.ReadTimeout, runtimeCfg.WriteTimeout)
	}
	if runtimeCfg.MultiStatements {
		t.Error("runtime MultiStatements = true, want false")
	}

	migCfg, err := migrationConfig(raw)
	if err != nil {
		t.Fatalf("migrationConfig() error = %v", err)
	}
	if migCfg.ReadTimeout != 0 || migCfg.WriteTimeout != 0 {
		t.Errorf("migration timeouts = %v/%v, want 0/0", migCfg.ReadTimeout, migCfg.WriteTimeout)
	}
	if !migCfg.MultiStatements {
		t.Error("migration MultiStatements = false, want true")
	}
	if migCfg.Timeout != 5*time.Second {
		t.Errorf("migration dial Timeout = %v, want 5s", migCfg.Timeout)
	}
}

// ─── integration tests: gated by TEST_MYSQL_DSN ──────────────────────────────
//
// L5: every integration test below shares the single schema named by
// TEST_MYSQL_DSN and destructively DROP TABLEs between cases. They are NOT
// process-parallel-safe: neither t.Parallel() within this package nor two
// concurrent `go test` processes against the same database. CI runs them in one
// sequential package run (the `migrations` job).

func TestMigrateFreshDatabase(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)

	result, err := Migrate(t.Context(), databaseURL(t, dsn))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	wantApplied := []string{"0001_baseline.sql", "0002_processed_messages.sql", "0003_contacts_map_index.sql", "0004_processed_messages_exact_collation.sql"}
	if !slices.Equal(result.Applied, wantApplied) {
		t.Errorf("Applied = %v, want %v", result.Applied, wantApplied)
	}

	requireTables(t, db, append(slices.Clone(legacyTables), "processed_messages", schemaMigrationsTable)...)

	// ADR-0004: the deterministic claim key must exist exactly as specified.
	assertIndex(t, db, "processed_messages", "uq_processed_claim",
		[]string{"connection_id", "source_system", "event_type", "source_message_id"}, true)

	// Audit R3/H2: the contacts_map index is non-unique on purpose (UNIQUE is
	// deferred until the production data-cleanup plan is approved).
	assertIndex(t, db, "contacts_map", "idx_contacts_map_conn_phone",
		[]string{"connectionId", "whatsappPhone"}, false)
}

func TestMigrateIdempotent(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	url := databaseURL(t, dsn)

	if _, err := Migrate(t.Context(), url); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	second, err := Migrate(t.Context(), url)
	if err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	if len(second.Applied) != 0 {
		t.Errorf("second run Applied = %v, want none", second.Applied)
	}
	wantSkipped := []string{"0001_baseline.sql", "0002_processed_messages.sql", "0003_contacts_map_index.sql", "0004_processed_messages_exact_collation.sql"}
	if !slices.Equal(second.Skipped, wantSkipped) {
		t.Errorf("second run Skipped = %v, want %v", second.Skipped, wantSkipped)
	}
}

func TestMigrateChecksumDrift(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	url := databaseURL(t, dsn)

	if _, err := Migrate(t.Context(), url); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		`UPDATE schema_migrations SET sha256 = ? WHERE filename = ?`,
		strings.Repeat("0", 64), "0002_processed_messages.sql"); err != nil {
		t.Fatalf("tampering with checksum: %v", err)
	}

	_, err := Migrate(t.Context(), url)
	if err == nil {
		t.Fatal("Migrate() error = nil, want checksum drift error")
	}
	if !strings.Contains(err.Error(), "checksum drift") {
		t.Errorf("Migrate() error = %v, want checksum drift", err)
	}
}

// TestMigrateLegacyDatabase reproduces a production database: legacy drizzle
// schema (bridge_settings missing, as the legacy migrations never created it),
// existing rows, and a __drizzle_migrations table that must stay untouched.
func TestMigrateLegacyDatabase(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	url := databaseURL(t, dsn)

	loadFixture(t, dsn, filepath.Join("testdata", "legacy_schema.sql"))
	createDrizzleTracking(t, db)

	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO connections
			(name, evolutionApiUrl, evolutionApiKey, evolutionInstanceName,
			 chatwootApiUrl, chatwootApiToken, chatwootAccountId, chatwootInboxId)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"synthetic-connection", "http://evolution.invalid", "synthetic",
		"synthetic", "http://chatwoot.invalid", "synthetic", 1, 1); err != nil {
		t.Fatalf("insert synthetic connection: %v", err)
	}

	result, err := Migrate(t.Context(), url)
	if err != nil {
		t.Fatalf("Migrate() on legacy schema error = %v", err)
	}
	wantApplied := []string{"0001_baseline.sql", "0002_processed_messages.sql", "0003_contacts_map_index.sql", "0004_processed_messages_exact_collation.sql"}
	if !slices.Equal(result.Applied, wantApplied) {
		t.Errorf("Applied = %v, want %v", result.Applied, wantApplied)
	}

	// The baseline heals the bridge_settings drift.
	requireTables(t, db, "bridge_settings", "processed_messages", schemaMigrationsTable)

	// Existing data survived the baseline.
	var name string
	if err := db.QueryRowContext(t.Context(), `SELECT name FROM connections WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("select synthetic connection: %v", err)
	}
	if name != "synthetic-connection" {
		t.Errorf("connection name = %q, want synthetic-connection", name)
	}

	// The legacy tracker is never touched.
	var drizzleRows int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM __drizzle_migrations`).Scan(&drizzleRows); err != nil {
		t.Fatalf("count __drizzle_migrations: %v", err)
	}
	if drizzleRows != 1 {
		t.Errorf("__drizzle_migrations rows = %d, want 1 (untouched)", drizzleRows)
	}
}

// TestMigratePartialSchemaFailsLoudly verifies that a database which is neither
// empty nor recognizably legacy stops the migrator instead of guessing.
func TestMigratePartialSchemaFailsLoudly(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)

	if _, err := db.ExecContext(t.Context(),
		"CREATE TABLE `users` (`id` int AUTO_INCREMENT NOT NULL, PRIMARY KEY (`id`))"); err != nil {
		t.Fatalf("create partial table: %v", err)
	}

	_, err := Migrate(t.Context(), databaseURL(t, dsn))
	if err == nil {
		t.Fatal("Migrate() error = nil, want partial/unknown state error")
	}
	if !strings.Contains(err.Error(), "partial/unknown") {
		t.Errorf("Migrate() error = %v, want partial/unknown state", err)
	}
}

// TestMigrateNeverCreatesDrizzleTracking locks down ADR-0002 §2: the migrator
// owns schema_migrations and must never create (or otherwise touch) the legacy
// drizzle tracker on a fresh database.
func TestMigrateNeverCreatesDrizzleTracking(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)

	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = '__drizzle_migrations'`).Scan(&count); err != nil {
		t.Fatalf("check __drizzle_migrations: %v", err)
	}
	if count != 0 {
		t.Errorf("__drizzle_migrations tables = %d, want 0 (migrator must never create it)", count)
	}
}

// TestMigratePreservesExistingBridgeSettings guards the heal path: on a legacy
// install where bridge_settings already exists with data (created out-of-band,
// as ADR-0002 documents), the baseline's IF NOT EXISTS must leave it untouched.
func TestMigratePreservesExistingBridgeSettings(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)

	loadFixture(t, dsn, filepath.Join("testdata", "legacy_schema.sql"))

	// Legacy install where the drifted table was created manually, with data.
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE bridge_settings (
			id int AUTO_INCREMENT NOT NULL,
			`+"`key`"+` varchar(100) NOT NULL,
			value text,
			updatedAt timestamp NOT NULL DEFAULT (now()) ON UPDATE CURRENT_TIMESTAMP,
			CONSTRAINT bridge_settings_id PRIMARY KEY (id),
			CONSTRAINT bridge_settings_key_unique UNIQUE (`+"`key`"+`)
		)`); err != nil {
		t.Fatalf("create bridge_settings: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO bridge_settings (`key`, value) VALUES ('synthetic-key', 'synthetic-value')"); err != nil {
		t.Fatalf("seed bridge_settings: %v", err)
	}

	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var value string
	if err := db.QueryRowContext(t.Context(),
		"SELECT value FROM bridge_settings WHERE `key` = 'synthetic-key'").Scan(&value); err != nil {
		t.Fatalf("bridge_settings row lost: %v", err)
	}
	if value != "synthetic-value" {
		t.Errorf("bridge_settings value = %q, want synthetic-value", value)
	}
}

// TestMigrateConcurrentInstances locks down the GET_LOCK serialization
// (ADR-0002 §1): two instances booting at once must apply each file exactly
// once between them, with no error and no double application.
func TestMigrateConcurrentInstances(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	url := databaseURL(t, dsn)

	const instances = 2
	results := make([]MigrateResult, instances)
	errs := make([]error, instances)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range instances {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = Migrate(t.Context(), url)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d Migrate() error = %v", i, err)
		}
	}
	total := 0
	for _, r := range results {
		total += len(r.Applied)
	}
	if total != 4 {
		t.Errorf("total files applied across instances = %d, want 4 (each applied exactly once)", total)
	}
}

// TestProcessedMessagesSingleClaim locks down the ADR-0004 atomic-claim
// contract: INSERT ... ON DUPLICATE KEY UPDATE status = status yields exactly
// one winner (affected rows 1) among concurrent claimers.
func TestProcessedMessagesSingleClaim(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	const workers = 8
	const claimSQL = `INSERT INTO processed_messages
		(connection_id, source_system, event_type, source_message_id, status, expires_at)
		VALUES (?, ?, ?, ?, 'processing', NOW(6) + INTERVAL 7 DAY)
		ON DUPLICATE KEY UPDATE status = status`

	ctx := t.Context()
	start := make(chan struct{})
	affected := make(chan int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := db.ExecContext(ctx, claimSQL, 1, "evolution", "message", "SYNTHETIC-MSG-1")
			if err != nil {
				errs <- err
				return
			}
			n, err := res.RowsAffected()
			if err != nil {
				errs <- err
				return
			}
			affected <- n
		}()
	}
	close(start)
	wg.Wait()
	close(affected)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent claim: %v", err)
	}
	var winners int
	for n := range affected {
		switch n {
		case 1:
			winners++
		case 0:
		default:
			t.Errorf("RowsAffected = %d, want 0 (duplicate) or 1 (winner)", n)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_messages`).Scan(&rows); err != nil {
		t.Fatalf("count processed_messages: %v", err)
	}
	if rows != 1 {
		t.Errorf("processed_messages rows = %d, want 1", rows)
	}
}

// TestProcessedMessagesExactIdentityCollation locks down the ADR-0004
// Amendment (review M1): identity columns are utf8mb4_bin, so two ids that
// differ only by case are two distinct claims instead of a false duplicate.
func TestProcessedMessagesExactIdentityCollation(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	if _, err := Migrate(t.Context(), databaseURL(t, dsn)); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// The collation change is applied to both identity columns.
	rows, err := db.QueryContext(t.Context(), `
		SELECT COLUMN_NAME, COLLATION_NAME FROM information_schema.columns
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'processed_messages'
		  AND COLUMN_NAME IN ('source_message_id', 'event_type')`)
	if err != nil {
		t.Fatalf("read column collations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var col, collation string
		if err := rows.Scan(&col, &collation); err != nil {
			t.Fatalf("scan collation: %v", err)
		}
		got[col] = collation
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate collations: %v", err)
	}
	for _, col := range []string{"source_message_id", "event_type"} {
		if got[col] != "utf8mb4_bin" {
			t.Errorf("collation of %s = %q, want utf8mb4_bin", col, got[col])
		}
	}

	const claimSQL = `INSERT INTO processed_messages
		(connection_id, source_system, event_type, source_message_id, status, expires_at)
		VALUES (?, ?, ?, ?, 'processing', NOW(6) + INTERVAL 7 DAY)
		ON DUPLICATE KEY UPDATE status = status`
	claim := func(id string) int64 {
		res, err := db.ExecContext(t.Context(), claimSQL, 1, "evolution", "message", id)
		if err != nil {
			t.Fatalf("claim %q: %v", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			t.Fatalf("affected rows for %q: %v", id, err)
		}
		return n
	}

	if n := claim("SYNTHETIC-AbC"); n != 1 {
		t.Errorf("first claim affected rows = %d, want 1", n)
	}
	if n := claim("synthetic-abc"); n != 1 {
		t.Errorf("case-different claim affected rows = %d, want 1 (distinct id, not a duplicate)", n)
	}
	if n := claim("SYNTHETIC-AbC"); n != 0 {
		t.Errorf("exact re-claim affected rows = %d, want 0 (true duplicate)", n)
	}

	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM processed_messages`).Scan(&count); err != nil {
		t.Fatalf("count processed_messages: %v", err)
	}
	if count != 2 {
		t.Errorf("processed_messages rows = %d, want 2", count)
	}
}

// TestMigrateResumesAfterDDLCommitWindow locks down M5: MySQL DDL auto-commits,
// so a crash between 0003's index creation and its schema_migrations insert must
// not wedge the migrator. A re-run that hits ER_DUP_KEYNAME on a known-additive
// file (with the index verified present) records the row instead of failing.
func TestMigrateResumesAfterDDLCommitWindow(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)
	url := databaseURL(t, dsn)

	if _, err := Migrate(t.Context(), url); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	// Simulate the crash: the index DDL committed, the tracking row did not.
	if _, err := db.ExecContext(t.Context(),
		`DELETE FROM schema_migrations WHERE filename = ?`, "0003_contacts_map_index.sql"); err != nil {
		t.Fatalf("simulate crash window: %v", err)
	}

	result, err := Migrate(t.Context(), url)
	if err != nil {
		t.Fatalf("Migrate() after crash window error = %v", err)
	}
	if !slices.Contains(result.Applied, "0003_contacts_map_index.sql") {
		t.Errorf("Applied = %v, want 0003 recorded", result.Applied)
	}
	assertIndex(t, db, "contacts_map", "idx_contacts_map_conn_phone",
		[]string{"connectionId", "whatsappPhone"}, false)
}

// TestMigrateSchemaConvergence locks down ADR-0002 §7 (review M4): the baseline
// applied to a fresh database must converge byte-for-byte with the legacy
// drizzle DDL for the 7 legacy tables, compared via SHOW CREATE TABLE.
func TestMigrateSchemaConvergence(t *testing.T) {
	db, dsn := testDB(t)
	cleanTables(t, db)

	legacy := []string{
		"users", "connections", "contacts_map", "message_logs",
		"admin_users", "notification_alerts", "notification_settings",
	}

	// (a) fresh database, baseline only.
	execEmbedded(t, dsn, "0001_baseline.sql")
	fresh := showCreateAll(t, db, legacy)

	// (b) legacy drizzle schema, then baseline only (every statement is
	// IF NOT EXISTS → no-op on existing tables, heals bridge_settings).
	cleanTables(t, db)
	loadFixture(t, dsn, filepath.Join("testdata", "legacy_schema.sql"))
	execEmbedded(t, dsn, "0001_baseline.sql")
	legacyDump := showCreateAll(t, db, legacy)

	for _, table := range legacy {
		if fresh[table] != legacyDump[table] {
			t.Errorf("SHOW CREATE TABLE %s differs between fresh baseline and legacy+baseline:\n fresh:  %s\n legacy: %s",
				table, fresh[table], legacyDump[table])
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// execEmbedded applies one embedded migration file (multiStatements), used by
// the convergence test to apply the baseline without the later increments.
func execEmbedded(t *testing.T, dsn, name string) {
	t.Helper()
	content, err := migrations.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open embedded connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), string(content)); err != nil {
		t.Fatalf("apply embedded %s: %v", name, err)
	}
}

// showCreateAll returns SHOW CREATE TABLE output per table (names are a
// hardcoded allowlist, never user input).
func showCreateAll(t *testing.T, db *sql.DB, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		var name, create string
		if err := db.QueryRowContext(t.Context(), "SHOW CREATE TABLE `"+table+"`").Scan(&name, &create); err != nil {
			t.Fatalf("SHOW CREATE TABLE %s: %v", table, err)
		}
		out[table] = create
	}
	return out
}

// testDB opens the dedicated integration database from TEST_MYSQL_DSN.
// Destructive fixtures run only against a *_test database.
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not set; skipping MySQL integration test")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("TEST_MYSQL_DSN is not a valid go-sql-driver DSN: %v", err)
	}
	if cfg.Net != "tcp" {
		t.Fatalf("TEST_MYSQL_DSN must use tcp, got net %q", cfg.Net)
	}
	if !strings.HasSuffix(cfg.DBName, "_test") {
		t.Fatalf("TEST_MYSQL_DSN must point at a dedicated *_test database, got %q", cfg.DBName)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Ping(t.Context(), db); err != nil {
		t.Fatalf("test database unreachable: %v", err)
	}
	return db, dsn
}

// databaseURL converts the driver DSN into the mysql:// URL Migrate expects.
func databaseURL(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u := &url.URL{Scheme: "mysql", Host: cfg.Addr, Path: "/" + cfg.DBName}
	if cfg.User != "" {
		if cfg.Passwd == "" {
			u.User = url.User(cfg.User)
		} else {
			u.User = url.UserPassword(cfg.User, cfg.Passwd)
		}
	}
	return u.String()
}

func allTestTables() []string {
	return append(slices.Clone(legacyTables), "processed_messages", schemaMigrationsTable, "__drizzle_migrations")
}

func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range allTestTables() {
		if _, err := db.ExecContext(t.Context(), "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
			t.Fatalf("drop table %s: %v", name, err)
		}
	}
}

func requireTables(t *testing.T, db *sql.DB, want ...string) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `
		SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	for _, name := range want {
		if !existing[name] {
			t.Errorf("table %s does not exist", name)
		}
	}
}

func assertIndex(t *testing.T, db *sql.DB, table, index string, wantCols []string, wantUnique bool) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `
		SELECT COLUMN_NAME, NON_UNIQUE
		FROM information_schema.statistics
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?
		ORDER BY SEQ_IN_INDEX`, table, index)
	if err != nil {
		t.Fatalf("read index %s: %v", index, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	var unique bool
	for rows.Next() {
		var col string
		var nonUnique int
		if err := rows.Scan(&col, &nonUnique); err != nil {
			t.Fatalf("scan index %s: %v", index, err)
		}
		cols = append(cols, col)
		unique = nonUnique == 0
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index %s: %v", index, err)
	}
	if len(cols) == 0 {
		t.Fatalf("index %s on %s does not exist", index, table)
	}
	if !slices.Equal(cols, wantCols) {
		t.Errorf("index %s columns = %v, want %v", index, cols, wantCols)
	}
	if unique != wantUnique {
		t.Errorf("index %s unique = %v, want %v", index, unique, wantUnique)
	}
}

func loadFixture(t *testing.T, dsn, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	cfg.MultiStatements = true // the fixture contains many statements
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), string(content)); err != nil {
		t.Fatalf("load legacy fixture: %v", err)
	}
}

func createDrizzleTracking(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE IF NOT EXISTS __drizzle_migrations (
			id bigint unsigned AUTO_INCREMENT PRIMARY KEY,
			hash text NOT NULL,
			created_at bigint
		)`); err != nil {
		t.Fatalf("create __drizzle_migrations: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO __drizzle_migrations (hash, created_at) VALUES ('synthetic-hash', 0)`); err != nil {
		t.Fatalf("seed __drizzle_migrations: %v", err)
	}
}
