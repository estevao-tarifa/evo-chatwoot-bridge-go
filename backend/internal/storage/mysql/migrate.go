package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/migrations"
)

const (
	// migrationsLockName serializes migration runs across instances.
	migrationsLockName    = "evo_bridge_migrate"
	migrationsLockTimeout = 10 // seconds

	schemaMigrationsTable = "schema_migrations"
)

// legacyTables are the tables the legacy installation uses (7 plus the
// never-migrated bridge_settings). Used only to tell a fresh database apart
// from a partial/unknown one; the baseline itself heals missing tables.
var legacyTables = []string{
	"users",
	"connections",
	"message_logs",
	"contacts_map",
	"admin_users",
	"notification_settings",
	"notification_alerts",
	"bridge_settings",
}

// createSchemaMigrationsDDL bootstraps the tracking table owned by the Go
// bridge. __drizzle_migrations stays untouched (ADR-0002 §2).
const createSchemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   VARCHAR(255) NOT NULL,
	sha256     CHAR(64)     NOT NULL,
	applied_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
	PRIMARY KEY (filename)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`

// migrationFilename enforces NNNN_slug.sql so application order is
// deterministic and a stray file fails loudly instead of being skipped.
var migrationFilename = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.sql$`)

// resumableAdditiveDDL names, per migration file, the secondary index whose
// creation is the file's only effect. MySQL DDL auto-commits, so a crash
// between the index creation and the schema_migrations insert (ADR-0002 §6)
// makes a re-run fail with ER_DUP_KEYNAME (1061). For these files that error
// means the change is already in place; we resume only when the expected index
// is actually present, so an unrelated duplicate-name error still fails loudly.
var resumableAdditiveDDL = map[string]struct{ table, index string }{
	"0003_contacts_map_index.sql": {table: "contacts_map", index: "idx_contacts_map_conn_phone"},
}

// migrationConfig derives the dedicated migration connection config from the
// runtime one. It enables multiStatements (the embedded static files only) and
// clears the 10s read/write timeouts: a long DDL on a production-sized table
// (an index build, a collation rebuild) outlives that bound and would abort
// mid-migration. The dial Timeout is kept. The runtime DSN policy is untouched.
func migrationConfig(rawURL string) (*mysqldriver.Config, error) {
	cfg, err := ConfigFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	cfg.MultiStatements = true
	cfg.ReadTimeout = 0
	cfg.WriteTimeout = 0
	return cfg, nil
}

// MigrateResult reports what Migrate did, in filename order.
type MigrateResult struct {
	Applied []string // files executed in this run
	Skipped []string // files already applied (checksum verified)
}

// Migrate applies the embedded SQL files to the database named in rawURL and
// records each file in schema_migrations (filename, sha256, applied_at).
//
// Properties (ADR-0002):
//   - forward-only: files are never re-executed or dropped;
//   - idempotent: a second run applies nothing;
//   - loud on drift: a changed checksum for an applied file is an error;
//   - serialized: GET_LOCK('evo_bridge_migrate') so two instances cannot race;
//   - fails loudly on a partially-created schema (neither empty nor legacy)
//     instead of guessing;
//   - __drizzle_migrations is never read, written or dropped.
//
// The files are static and embedded; a dedicated connection with
// multiStatements=true executes one whole file per transaction. MySQL DDL
// auto-commits, so the tracking row is written last: a crash mid-file is
// retried as a whole run (the baseline is IF NOT EXISTS; see the rollback
// notes in each file for the DDL window).
func Migrate(ctx context.Context, rawURL string) (MigrateResult, error) {
	var result MigrateResult

	cfg, err := migrationConfig(rawURL)
	if err != nil {
		return result, err
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return result, fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		return result, fmt.Errorf("reserve migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := acquireLock(ctx, conn); err != nil {
		return result, err
	}
	defer releaseLock(ctx, conn)

	if _, err := conn.ExecContext(ctx, createSchemaMigrationsDDL); err != nil {
		return result, fmt.Errorf("bootstrap %s: %w", schemaMigrationsTable, err)
	}

	applied, err := appliedChecksums(ctx, conn)
	if err != nil {
		return result, err
	}
	if len(applied) == 0 {
		if err := checkSchemaState(ctx, conn); err != nil {
			return result, err
		}
	}

	files, err := migrationFiles()
	if err != nil {
		return result, err
	}

	for _, name := range files {
		content, err := migrations.FS.ReadFile(name)
		if err != nil {
			return result, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(content)
		checksum := hex.EncodeToString(sum[:])

		if recorded, ok := applied[name]; ok {
			if recorded != checksum {
				return result, fmt.Errorf(
					"migration %s checksum drift: recorded %s, file is %s; restore the original file or add a new numbered migration — never edit an applied one",
					name, recorded, checksum)
			}
			result.Skipped = append(result.Skipped, name)
			continue
		}

		if err := applyFile(ctx, conn, name, string(content), checksum); err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, name)
	}

	return result, nil
}

// execer is the subset of *sql.Conn / *sql.Tx used to record a migration row.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// applyFile executes one whole migration file (multiStatements) and records it
// in the same transaction, so a file is only marked applied after it succeeded.
func applyFile(ctx context.Context, conn *sql.Conn, name, content, checksum string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, content); err != nil {
		// MySQL implicitly commits DDL, so Rollback only covers DML; the
		// tracking row is written last and a failed run retries as a whole.
		_ = tx.Rollback()
		resumable, ok := resumableAdditiveDDL[name]
		if !ok || !isDuplicateIndexName(err) {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		// A crash between the index DDL and the tracking insert re-runs this
		// file against an index that already exists. Record it only once the
		// expected index is verified present, so a real collision still fails.
		present, checkErr := indexExists(ctx, conn, resumable.table, resumable.index)
		if checkErr != nil {
			return fmt.Errorf("verify resumed %s: %w", name, checkErr)
		}
		if !present {
			return fmt.Errorf("apply %s: %w (expected index %s.%s is missing; refusing to record)",
				name, err, resumable.table, resumable.index)
		}
		return recordApplied(ctx, conn, name, checksum)
	}
	if err := recordApplied(ctx, tx, name, checksum); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

func recordApplied(ctx context.Context, e execer, name, checksum string) error {
	const insert = `INSERT INTO ` + schemaMigrationsTable + ` (filename, sha256) VALUES (?, ?)`
	if _, err := e.ExecContext(ctx, insert, name, checksum); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

// isDuplicateIndexName reports whether err is MySQL ER_DUP_KEYNAME (1061).
func isDuplicateIndexName(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1061
}

// indexExists reports whether a secondary index is present, used to confirm the
// crash-window resume is safe before recording the migration row.
func indexExists(ctx context.Context, conn *sql.Conn, table, index string) (bool, error) {
	var count int
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.statistics
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`,
		table, index).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check index %s.%s: %w", table, index, err)
	}
	return count > 0, nil
}

// acquireLock takes the MySQL advisory lock; without it two instances booting
// at once could both decide a file is pending.
func acquireLock(ctx context.Context, conn *sql.Conn) error {
	var acquired sql.NullInt64
	err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrationsLockName, migrationsLockTimeout).Scan(&acquired)
	if err != nil {
		return fmt.Errorf("acquire migration lock %q: %w", migrationsLockName, err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("migration lock %q is held by another instance; retry later", migrationsLockName)
	}
	return nil
}

// releaseLock is best effort: closing the connection releases the lock anyway.
func releaseLock(ctx context.Context, conn *sql.Conn) {
	var released sql.NullInt64
	// WithoutCancel: the lock must be freed even when ctx was cancelled.
	_ = conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", migrationsLockName).Scan(&released)
}

// checkSchemaState fails loudly on databases that are neither empty nor
// recognizably legacy (ADR-0002 §3): guessing could hide a typo'd database
// name or a half-restored backup. Runs only when schema_migrations is empty.
// Requiring `connections` matches the legacy signal (users exists from the
// first legacy migration, but connections is the table every legacy path uses).
func checkSchemaState(ctx context.Context, conn *sql.Conn) error {
	existing, err := existingTables(ctx, conn)
	if err != nil {
		return err
	}
	if existing["connections"] {
		return nil // legacy installation (or already baseline-shaped)
	}

	var found []string
	for _, table := range legacyTables {
		if existing[table] {
			found = append(found, table)
		}
	}
	if len(found) == 0 {
		return nil // fresh/empty database
	}
	return fmt.Errorf("partial/unknown schema state: found %s but not connections; refusing to guess, review the database manually (ADR-0002)", strings.Join(found, ", "))
}

func existingTables(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = DATABASE()`)
	if err != nil {
		return nil, fmt.Errorf("list existing tables: %w", err)
	}
	defer func() { _ = rows.Close() }()

	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	return existing, nil
}

func appliedChecksums(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT filename, sha256 FROM `+schemaMigrationsTable)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", schemaMigrationsTable, err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]string)
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return nil, fmt.Errorf("scan %s: %w", schemaMigrationsTable, err)
		}
		applied[name] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", schemaMigrationsTable, err)
	}
	return applied, nil
}

// migrationFiles lists the embedded files in application order, rejecting any
// name that does not follow NNNN_slug.sql.
func migrationFiles() ([]string, error) {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(files)
	for _, name := range files {
		if !migrationFilename.MatchString(name) {
			return nil, fmt.Errorf("migration file %q does not match NNNN_slug.sql", name)
		}
	}
	return files, nil
}
