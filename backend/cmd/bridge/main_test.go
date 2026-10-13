package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestCheckHealth(t *testing.T) {
	t.Run("healthy server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("path = %q, want /healthz", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		port := serverPort(t, srv.URL)
		if err := checkHealth(port); err != nil {
			t.Fatalf("checkHealth(%d) error = %v", port, err)
		}
	})

	t.Run("unhealthy status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		if err := checkHealth(serverPort(t, srv.URL)); err == nil {
			t.Fatal("checkHealth() error = nil, want error for 503")
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		port := freePort(t)
		if err := checkHealth(port); err == nil {
			t.Fatalf("checkHealth(%d) error = nil, want connection error", port)
		}
	})
}

func serverPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing test server url %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing test server port %q: %v", u.Port(), err)
	}
	return port
}

// freePort returns a port that was just closed, i.e. (almost certainly)
// nothing is listening on it.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocating free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// bridgeBin is built once for the process-level tests: run()'s signal handling
// and fail-fast paths can only be exercised by executing the real binary.
var bridgeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bridge-process-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating temp dir:", err)
		os.Exit(1)
	}
	bridgeBin = filepath.Join(dir, "bridge")
	if out, err := exec.Command("go", "build", "-o", bridgeBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building bridge binary: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// startBridge launches the binary with a hermetic environment (only PORT plus
// the extra variables) and captures stdout and stderr together.
func startBridge(t *testing.T, port int, extraEnv ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(bridgeBin)
	cmd.Env = append([]string{"PORT=" + strconv.Itoa(port)}, extraEnv...)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting bridge: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil { // the test failed before cmd.Wait
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, &logs
}

// waitForHealthz polls the real /healthz endpoint until the server is up.
func waitForHealthz(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := checkHealth(port); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bridge on port %d never became healthy", port)
}

func TestProcessGracefulShutdown(t *testing.T) {
	port := freePort(t)
	cmd, logs := startBridge(t, port, "LOG_LEVEL=info", "APP_ENV=development")
	waitForHealthz(t, port)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("bridge did not exit 0 on SIGTERM: %v\nlogs:\n%s", err, logs.String())
	}

	out := logs.String()
	for _, want := range []string{
		`"msg":"shutdown signal received, draining"`,
		`"msg":"shutdown complete"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("shutdown logs missing %s:\n%s", want, out)
		}
	}
}

// TestProcessBindFailureFailsFast proves a bind failure exits non-zero and is
// reported before "listening" is logged (L1).
func TestProcessBindFailureFailsFast(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(bridgeBin)
	cmd.Env = []string{"PORT=" + strconv.Itoa(port), "APP_ENV=development"}
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run() error = %v, want a non-zero exit for a bind failure\noutput:\n%s", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), `"msg":"http server bind failed"`) {
		t.Errorf("bind failure was not logged:\n%s", out)
	}
	if strings.Contains(string(out), `"msg":"http server listening"`) {
		t.Errorf("logged listening despite a failed bind:\n%s", out)
	}
}

func TestProcessLogLevelWarnSuppressesInfo(t *testing.T) {
	port := freePort(t)
	cmd, logs := startBridge(t, port, "LOG_LEVEL=warn", "APP_ENV=development")
	waitForHealthz(t, port)

	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/healthz", "/nope"} {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("bridge did not exit 0 on SIGTERM: %v", err)
	}

	out := logs.String()
	if strings.Contains(out, `"level":"INFO"`) {
		t.Errorf("LOG_LEVEL=warn still emitted INFO logs:\n%s", out)
	}
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"path":"/nope"`) {
		t.Errorf("expected the 404 access log line at warn level:\n%s", out)
	}
}

func TestProcessInvalidConfigFailsFast(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		wantErr string
	}{
		{name: "port is not a number", env: []string{"PORT=abc"}, wantErr: "PORT"},
		{name: "database url is not mysql", env: []string{"DATABASE_URL=postgres://user:pass@db:5432/bridge"}, wantErr: "DATABASE_URL"},
		{name: "admin user without password", env: []string{"ADMIN_USER=admin"}, wantErr: "ADMIN_USER and ADMIN_PASS"},
		{name: "unknown log level", env: []string{"LOG_LEVEL=verbose"}, wantErr: "LOG_LEVEL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(bridgeBin)
			cmd.Env = tt.env // hermetic: unlisted variables stay unset
			out, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run() error = %v, want a non-zero exit\noutput:\n%s", err, out)
			}
			if exitErr.ExitCode() != 1 {
				t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
			}
			for _, want := range []string{`"msg":"invalid configuration"`, tt.wantErr} {
				if !strings.Contains(string(out), want) {
					t.Errorf("stderr missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// ─── migrate subcommand ──────────────────────────────────────────────────────

// TestRunMigrateMissingDatabaseURL is a unit test for the testable core: an
// empty DATABASE_URL fails with a clear error instead of panicking.
func TestRunMigrateMissingDatabaseURL(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	err := runMigrate(t.Context(), "", logger)
	if err == nil {
		t.Fatal("runMigrate() error = nil, want a DATABASE_URL error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("runMigrate() error = %v, want it to name DATABASE_URL", err)
	}
	if logs.Len() != 0 {
		t.Errorf("runMigrate() logged %q, want nothing before failing", logs.String())
	}
}

// TestLogMigrationTarget proves the pre-run target line identifies where the
// migration is pointed (host + database) and never carries the credentials.
func TestLogMigrationTarget(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	logMigrationTarget(logger, "mysql://bridge:sup3r-s3cret@db.internal:3306/evo_bridge")

	out := logs.String()
	for _, want := range []string{`"msg":"migrating"`, "db.internal:3306", "evo_bridge"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
	for _, leaked := range []string{"sup3r-s3cret", "bridge:"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked credentials (%q): %q", leaked, out)
		}
	}
}

// TestProcessMigrateDispatch proves `bridge migrate` is dispatched as a
// subcommand: with DATABASE_URL unset it must exit non-zero with a clear error
// (not panic, not start the server).
func TestProcessMigrateDispatch(t *testing.T) {
	cmd := exec.Command(bridgeBin, "migrate")
	cmd.Env = []string{} // hermetic: DATABASE_URL is unset
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("migrate error = %v, want a non-zero exit\noutput:\n%s", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	for _, want := range []string{`"msg":"migration failed"`, "DATABASE_URL"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), `"msg":"http server listening"`) {
		t.Errorf("migrate started the server:\n%s", out)
	}
}

// TestProcessUnknownSubcommandRunsServer proves an unrecognized first argument
// is not a subcommand: main falls through to run(), so today's behaviour (the
// server path, here its invalid-config fail-fast) is unchanged.
func TestProcessUnknownSubcommandRunsServer(t *testing.T) {
	cmd := exec.Command(bridgeBin, "not-a-subcommand")
	cmd.Env = []string{"PORT=abc"} // invalid server config: only run() validates it
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want a non-zero exit\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), `"msg":"invalid configuration"`) {
		t.Errorf("unknown subcommand did not take the server path:\n%s", out)
	}
}

// ─── migrate subcommand integration (gated by TEST_MYSQL_DSN) ───────────────

// migrateSummary is the structured line `bridge migrate` logs on success.
type migrateSummary struct {
	Msg     string   `json:"msg"`
	Applied []string `json:"applied"`
	Skipped []string `json:"skipped"`
}

var wantMigrationFiles = []string{
	"0001_baseline.sql",
	"0002_processed_messages.sql",
	"0003_contacts_map_index.sql",
	"0004_processed_messages_exact_collation.sql",
}

// TestProcessMigrateAppliesThenSkips is the DSN-guarded integration test: a
// fresh *_test database gets every migration on the first run and none on the
// second (Skipped = all, Applied empty). The checksum-drift case lives in
// internal/storage/mysql/migrate_test.go and is not duplicated here.
func TestProcessMigrateAppliesThenSkips(t *testing.T) {
	db, databaseURL := migrateTestDB(t)

	first := execMigrate(t, databaseURL)
	if !slices.Equal(first.Applied, wantMigrationFiles) {
		t.Errorf("first run applied = %v, want %v", first.Applied, wantMigrationFiles)
	}
	if len(first.Skipped) != 0 {
		t.Errorf("first run skipped = %v, want none", first.Skipped)
	}

	for _, name := range []string{"processed_messages", "schema_migrations"} {
		if !tableExists(t, db, name) {
			t.Errorf("table %s does not exist after migrate", name)
		}
	}

	second := execMigrate(t, databaseURL)
	if len(second.Applied) != 0 {
		t.Errorf("second run applied = %v, want none", second.Applied)
	}
	if !slices.Equal(second.Skipped, wantMigrationFiles) {
		t.Errorf("second run skipped = %v, want %v", second.Skipped, wantMigrationFiles)
	}
}

// migrateTestDB opens the dedicated integration database from TEST_MYSQL_DSN and
// clears it. It enforces the same safety policy as the storage layer: tcp only
// and a *_test database, never the legacy production schema.
func migrateTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not set; skipping migrate subcommand integration test")
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
	// The helper DROPs every table in the schema. A remote DSN whose database
	// name happens to end in _test must never be wiped by accident: require a
	// loopback host unless the operator explicitly opts in.
	if !loopbackHost(cfg.Addr) && os.Getenv("TEST_MYSQL_ALLOW_REMOTE") != "1" {
		t.Skipf("TEST_MYSQL_DSN host %q is not loopback; set TEST_MYSQL_ALLOW_REMOTE=1 to allow a destructive run against it", cfg.Addr)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("test database unreachable: %v", err)
	}
	dropAllTables(t, db)

	u := &url.URL{Scheme: "mysql", Host: cfg.Addr, Path: "/" + cfg.DBName}
	if cfg.User != "" {
		if cfg.Passwd == "" {
			u.User = url.User(cfg.User)
		} else {
			u.User = url.UserPassword(cfg.User, cfg.Passwd)
		}
	}
	return db, u.String()
}

// execMigrate runs the built binary as `bridge migrate` against databaseURL and
// returns the parsed summary line. The binary is the scratch entrypoint, so this
// exercises the same shape as `docker run <image> migrate`.
func execMigrate(t *testing.T, databaseURL string) migrateSummary {
	t.Helper()
	cmd := exec.Command(bridgeBin, "migrate")
	cmd.Env = []string{"DATABASE_URL=" + databaseURL}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bridge migrate error = %v\noutput:\n%s", err, out)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var summary migrateSummary
		if json.Unmarshal([]byte(line), &summary) == nil && summary.Msg == "migrations applied" {
			return summary
		}
	}
	t.Fatalf("no migration summary in output:\n%s", out)
	return migrateSummary{}
}

func dropAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close table list: %v", err)
	}
	for _, name := range names {
		if _, err := db.ExecContext(t.Context(), "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
			t.Fatalf("drop table %s: %v", name, err)
		}
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return count > 0
}

// loopbackHost reports whether addr (host:port) targets the local machine.
func loopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
