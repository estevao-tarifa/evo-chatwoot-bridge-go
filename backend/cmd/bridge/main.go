// Command bridge runs the evo-chatwoot-bridge backend: configuration, HTTP
// server and graceful shutdown. Storage and business logic are wired in later
// phases at the marked wiring point.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed zoneinfo: the runtime image is scratch (ADR-0007)

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/config"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/httpapi"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/observability"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/storage/mysql"
	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/worker"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const (
	shutdownTimeout    = 10 * time.Second
	healthcheckTimeout = 3 * time.Second
)

func main() {
	// Subcommands. An unrecognized first argument is not a subcommand: it falls
	// through to the server so existing invocations keep their behaviour.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "migrate":
			os.Exit(migrate())
		}
	}
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger().Error("invalid configuration", "error", err)
		return 1
	}

	level, err := observability.ParseLevel(cfg.LogLevel)
	if err != nil {
		// config.Load already validates the level; the guard stays as defence.
		bootstrapLogger().Error("invalid log level", "error", err)
		return 1
	}
	logger := observability.NewLogger(os.Stdout, level)

	// The worker pool runs jobs on its own process context, never the signal
	// context: SIGTERM must stop intake and let the drain finish before
	// in-flight jobs are aborted (ADR-0005).
	processCtx, processCancel := context.WithCancel(context.Background())
	defer processCancel()

	// ─── DB wiring ──────────────────────────────────────────────────────────
	// A configured DATABASE_URL opens the pool; /readyz pings it on every
	// probe, so MySQL being down at boot degrades readiness instead of
	// crashing the process. Migrations run separately (storage/mysql.Migrate).
	var ready httpapi.ReadinessFunc
	var webhookHandler http.Handler
	var chatwootWebhookHandler http.Handler
	var queue *worker.Queue
	var adminDeps *httpapi.AdminDeps
	if cfg.DatabaseURL != "" {
		pool, err := mysql.Open(cfg.DatabaseURL)
		if err != nil {
			logger.Error("invalid database configuration", "error", err)
			return 1
		}
		defer func() { _ = pool.Close() }() // best effort at process exit

		if err := mysql.Ping(context.Background(), pool); err != nil {
			logger.Warn("database not reachable yet, /readyz will report unavailable", "error", err)
		}
		ready = func(ctx context.Context) error { return mysql.Ping(ctx, pool) }

		// Per-connection runtime registry: compose the bridge.Engine for every
		// active connection (wiring.go) and register the webhook route.
		reg, err := buildRegistry(context.Background(), pool, logger)
		if err != nil {
			logger.Error("connection registry build failed", "error", err)
			return 1
		}

		// Boot reconciliation: RecoverStale → Reclaim → MarkUnconfirmed, never
		// a send. It runs before the webhook route accepts anything.
		if engine := reg.anyEngine(); engine != nil {
			if err := reconcileStale(context.Background(), mysql.NewProcessedMessages(pool), engine.ReconcileStale, cfg.StaleAge, logger); err != nil {
				logger.Warn("boot reconcile failed", "error", err)
			}
		} else {
			logger.Warn("boot reconcile skipped: no wired active connection")
		}

		// Bounded worker pool (ADR-0005).
		queue = worker.New(worker.Config{
			Capacity: cfg.QueueCapacity,
			MaxBytes: cfg.QueueMaxBytes,
			Workers:  cfg.Workers,
		}, logger)
		queue.Start(processCtx)

		if cfg.WebhookTokenSecret == "" {
			logger.Warn("WEBHOOK_TOKEN_SECRET is not set; the Evolution webhook route will answer 503")
		}
		webhookHandler = httpapi.NewWebhookHandler(httpapi.WebhookOptions{
			Secret:   cfg.WebhookTokenSecret,
			Registry: reg,
			Store:    mysql.NewDedupAdapter(pool),
			Enqueuer: queue,
			Logger:   logger,
		})
		// The Chatwoot route shares the derived path token. When
		// CHATWOOT_WEBHOOK_SECRET is unset it is unsigned token-only (the
		// API-inbox default); when set it requires a valid account HMAC and
		// fails closed (ADR-0006 Amendment 3).
		if cfg.ChatwootWebhookSecret != "" {
			logger.Warn("CHATWOOT_WEBHOOK_SECRET is set; the Chatwoot route requires a valid HMAC signature")
		}
		chatwootWebhookHandler = httpapi.NewChatwootWebhookHandler(httpapi.ChatwootWebhookOptions{
			Secret:         cfg.WebhookTokenSecret,
			ChatwootSecret: cfg.ChatwootWebhookSecret,
			Registry:       reg,
			Store:          mysql.NewDedupAdapter(pool),
			Enqueuer:       queue,
			Logger:         logger,
		})

		// Phase 5 admin surface: auth, stores and the upstream factory.
		if err := bootstrapAdmin(context.Background(), pool, cfg, logger); err != nil {
			logger.Warn("admin bootstrap failed", "error", err)
		}
		adminDeps = buildAdminDeps(cfg, pool, logger)
	}
	// ────────────────────────────────────────────────────────────────────────

	srv := httpapi.NewServer(httpapi.Options{
		Addr:               ":" + strconv.Itoa(cfg.Port),
		Logger:             logger,
		Ready:              ready,
		Version:            version,
		Commit:             commit,
		Webhook:            webhookHandler,
		ChatwootWebhook:    chatwootWebhookHandler,
		Admin:              adminDeps,
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
	})

	logger.Info("bridge starting",
		"version", version,
		"commit", commit,
		"port", cfg.Port,
		"log_level", cfg.LogLevel,
		"app_env", cfg.AppEnv,
		"cors_allowed_origins", len(cfg.CORSAllowedOrigins),
		"database_configured", cfg.DatabaseURL != "",
		"webhook_ingress", webhookHandler != nil,
		"admin_bootstrap_configured", cfg.AdminUser != "",
	)

	// Bind before logging "listening": a bind failure must be reported, not
	// raced by a concurrent shutdown signal and masked by exit 0.
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logger.Error("http server bind failed", "addr", srv.Addr, "error", err)
		return 1
	}
	logger.Info("http server listening", "addr", listener.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		logger.Error("http server failed", "error", err)
		return 1
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining", "timeout", shutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed, forcing close", "error", err)
		_ = srv.Close() // best effort: unblock any lingering connections
		return 1
	}

	// Stop intake, then drain the queue within the drain budget. On expiry the
	// process context is cancelled so aborted jobs leave their rows
	// 'processing' for the next-boot reconcile (ADR-0005).
	if queue != nil {
		queue.Stop()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.DrainTimeout)
		defer drainCancel()
		if err := queue.Wait(drainCtx); err != nil {
			logger.Warn("worker drain timed out; aborting in-flight jobs", "error", err)
			processCancel()
		} else {
			logger.Info("worker drained")
		}
	}

	// A serve error racing the shutdown signal must not be masked by exit 0.
	select {
	case err := <-errCh:
		logger.Error("http server failed", "error", err)
		return 1
	default:
	}

	logger.Info("shutdown complete")
	return 0
}

// healthcheck probes the local /healthz endpoint. The runtime image is
// scratch (no curl/wget), so the container HEALTHCHECK calls the binary
// itself: `bridge healthcheck` (ADR-0007).
func healthcheck() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	if err := checkHealth(cfg.Port); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	return 0
}

func checkHealth(port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// migrate applies the forward-only schema migrations to DATABASE_URL and exits.
// The runtime image is scratch (no shell), so this subcommand is the migration
// entrypoint: `docker run --rm <image> migrate`, or a one-shot compose service
// (`docker compose run --rm bridge migrate`). It needs only DATABASE_URL — not
// the rest of the server config — so migrations can run before the webhook
// secrets are provisioned. The summary and any error are logged without the DSN
// (ADR-0002, ADR-0007).
//
// Ctrl-C during a migration is safe: the connection close releases
// GET_LOCK('evo_bridge_migrate'), DDL rolls back on its own, and a re-run
// resumes where it stopped (the baseline is IF NOT EXISTS; the tracking row is
// written last).
func migrate() int {
	logger := bootstrapLogger()
	if err := runMigrate(context.Background(), strings.TrimSpace(os.Getenv("DATABASE_URL")), logger); err != nil {
		logger.Error("migration failed", "error", err)
		return 1
	}
	return 0
}

// logMigrationTarget logs the migration target — host and database only — so a
// 3am operator can see where the run is pointed without the credentials ever
// reaching the log. A URL that does not parse is left for mysql.Migrate, whose
// error is redacted.
func logMigrationTarget(logger *slog.Logger, databaseURL string) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return
	}
	logger.Info("migrating", "host", u.Host, "database", strings.TrimPrefix(u.Path, "/"))
}

// runMigrate is the testable core of `bridge migrate`: it fails fast on a
// missing DATABASE_URL and otherwise reports only the applied and skipped file
// names, never the DSN.
func runMigrate(ctx context.Context, databaseURL string, logger *slog.Logger) error {
	if databaseURL == "" {
		return errors.New("DATABASE_URL is not set")
	}

	logMigrationTarget(logger, databaseURL)
	logger.Info("migrations starting")

	result, err := mysql.Migrate(ctx, databaseURL)
	if err != nil {
		return err
	}

	logger.Info("migrations applied", "applied", result.Applied, "skipped", result.Skipped)
	return nil
}

// bootstrapLogger is used before the configured logger exists.
func bootstrapLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}
