// Command server is MRF Sentinel's entry point: it wires together config,
// the database, the mailer, the background validation worker, and the HTTP
// router, then serves.
//
// There's no Spring-style application context building this graph for
// you — main is where it's built, by hand, once, in the order each piece
// actually needs its dependencies. That's normal for Go: explicit
// construction here is the whole of what a DI framework would otherwise
// be doing behind the scenes.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
	"github.com/bobbylon127/mrfsentinel/internal/config"
	"github.com/bobbylon127/mrfsentinel/internal/store"
	"github.com/bobbylon127/mrfsentinel/internal/validation"
	"github.com/bobbylon127/mrfsentinel/internal/web"
)

// main is the process entry point, and deliberately does almost nothing:
// build a logger, hand off to run, and translate a returned error into a
// nonzero exit status. All the real startup work lives in run so that its
// deferred cleanup (closing the database pool, cancelling the startup
// context) actually executes — os.Exit skips deferred functions entirely,
// so calling it from inside the wiring below would silently leak the very
// things those defers exist to release.
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("fatal startup error", "err", err)
		os.Exit(1)
	}
}

// run builds this app's entire dependency graph by hand, in the order each
// piece needs the one before it — config, database pool, schema
// migrations, store, mailer, background worker, handlers, router — and
// then serves until a shutdown signal arrives or the listener fails.
//
// It returns an error instead of exiting so that main owns the exit status,
// and so that every defer registered here still runs on the way out; see
// main's comment for why that distinction matters.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// A short-lived context just for startup work (connecting and
	// migrating) — separate from the long-lived server below, which runs
	// until it's told to stop.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startupCancel()

	if err := waitForDatabase(startupCtx, db, logger); err != nil {
		return err
	}
	if err := store.Migrate(startupCtx, db); err != nil {
		return err
	}
	logger.Info("database ready and migrated")

	st := store.NewStore(db)
	mailer := auth.Mailer{Host: cfg.SMTPHost, Port: cfg.SMTPPort, From: cfg.SMTPFrom}
	worker := validation.NewWorker(st, cfg.MaxMRFBytes, cfg.FetchTimeout, logger)

	handlers, err := web.NewHandlers(st, mailer, worker, cfg, logger)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           web.NewRouter(handlers),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Listen for Ctrl+C / SIGTERM and shut the server down gracefully
	// rather than dropping in-flight requests — the Go stdlib equivalent
	// of Spring Boot's graceful-shutdown behavior, which is opt-in
	// configuration there but a few explicit lines here.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// waitForDatabase retries the initial connection for a few seconds. In
// docker-compose.yml, Postgres and this app start at roughly the same
// time; depends_on with a healthcheck (see that file) already makes Docker
// wait for Postgres's own readiness, but this retry loop is a second,
// cheap safety net against the narrow window where the container is
// marked healthy but a brand-new Postgres is still finishing its own
// startup.
func waitForDatabase(ctx context.Context, db interface{ PingContext(context.Context) error }, logger *slog.Logger) error {
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		logger.Warn("database not ready yet, retrying", "err", lastErr)
		time.Sleep(1 * time.Second)
	}
	return lastErr
}
