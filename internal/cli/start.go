package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api"
	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/db"
	dockerdrv "github.com/waltzofpearls/deepcove/internal/sandbox/docker"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/ui"
)

// cmdStart runs the deepcove background service. Default for solo and
// most users. See SPEC.md §4.1.1.
//
// Currently mounts:
//   - DuckDB store + migrations
//   - Docker sandbox driver
//   - Session lifecycle service on /api/sessions
//   - local TCP listener on cfg.Addr serving both API and the SPA
//
// Still TODO:
//   - First-run owner identity persistence
//   - Unix socket listener (so CLI doesn't need to dial TCP)
//   - tsnet TLS listener with WhoIs identity middleware
//   - Peer discovery, cross-node fan-out
//   - Session attach (PTY WebSocket)
func cmdStart(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := newLogger(cfg)

	sqlDB, err := db.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer sqlDB.Close()
	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("database ready", "data_dir", cfg.DataDir)

	// Sandbox driver: Docker is the v1 backend (SPEC §7.2). The
	// session service bind-mounts $DataDir/sessions/<id>/ into
	// each sandbox; that root is the only host path it's allowed
	// to share, enforced by the sandbox layer's mount-source
	// validator.
	workspaceRoot := filepath.Join(cfg.DataDir, "sessions")
	sandbox, err := dockerdrv.New(dockerdrv.WithAllowedMountRoots(workspaceRoot))
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	defer sandbox.Close()

	// Persistence: domain-owned SessionRepository over the shared
	// DuckDB handle. Each domain package owns its own CRUD wrapper
	// (cohesion by bounded context) — `internal/db` is just the
	// connection lifecycle.
	sessionRepo := session.NewDuckDBSessionRepository(sqlDB)
	sessionSvc := session.NewService(sessionRepo, sandbox, cfg.DataDir, logger)

	mux := http.NewServeMux()
	if err := api.New(
		api.WithConfig(cfg),
		api.WithMux(mux),
		api.WithVersion(Version),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	sessionSvc.AttachRoutes(mux)
	if err := ui.New(ui.WithConfig(cfg), ui.WithMux(mux)).AttachRoutes(); err != nil {
		return fmt.Errorf("ui: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	// Timeouts: even though this listener is local (127.0.0.1) by
	// default, no-timeout net/http servers are a slowloris foot-gun
	// and a curl --limit-rate against localhost is enough to lock up
	// a goroutine indefinitely. ReadHeaderTimeout is the load-bearing
	// one; the others bound long-lived but stalled streams.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	url := "http://" + cfg.Addr + "/"

	// Print the URL prominently so it's easy to copy/paste — the server
	// runs in the foreground and the browser is the primary entry point.
	fmt.Println()
	fmt.Printf("  deepcove:  %s\n", url)
	fmt.Println()

	logger.Info("server listening", "url", url, "is_dev", cfg.IsDev)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received; stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// Wait for the Serve goroutine to actually exit, so cmdStart
		// doesn't return while a stray goroutine is still cleaning
		// up listeners or in-flight connections.
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}
