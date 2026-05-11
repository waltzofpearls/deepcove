package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api"
	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/repository"
	"github.com/waltzofpearls/deepcove/internal/ui"
)

// cmdStart runs the deepcove background service. Default for solo and
// most users. See SPEC.md §4.1.1.
//
// Currently mounts:
//   - local TCP listener on cfg.Addr serving both API (via internal/api)
//     and the SPA (via internal/ui)
//
// Still TODO:
//   - Open DuckDB (internal/repository) at first run, persist owner identity
//   - Start Docker sandbox driver (internal/sandbox)
//   - Bring up Unix socket listener for CLI
//   - Bring up tsnet TLS listener with WhoIs identity middleware
//   - Peer discovery, fan-out, etc.
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

	db, err := repository.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	defer db.Close()
	if err := repository.Migrate(db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("database ready", "data_dir", cfg.DataDir)

	mux := http.NewServeMux()
	if err := api.New(
		api.WithConfig(cfg),
		api.WithMux(mux),
		api.WithVersion(Version),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
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
