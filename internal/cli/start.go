package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api"
	"github.com/waltzofpearls/deepcove/internal/api/middleware"
	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/ui"
	"golang.org/x/sync/errgroup"
)

// cmdStart runs the deepcove background service. Default for solo and most users.
//
// Mounts the three front doors over one mux:
//   - local TCP on cfg.Addr: SPA + runtime API, behind LocalGate (Host allowlist + per-run token)
//   - Unix socket: full runtime API for the local CLI (filesystem perms)
//   - tailnet (via connect.Manager): owner-gated, when connected
//
// Backed by DuckDB + migrations, the Docker sandbox driver, and the session lifecycle service.
//
// Still TODO:
//   - Peer discovery, cross-node fan-out
func cmdStart(ctx context.Context, args []string) error {
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	// default to the env value so an explicit --cluster overrides DEEPCOVE_CLUSTER
	fs.StringVar(&cfg.Cluster, "cluster", cfg.Cluster, `cluster label to group this node with your others (persisted; default "default")`)
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := newLogger(cfg)

	sqlDB, err := db.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("database ready", "data_dir", cfg.DataDir)

	// Resolve this node's cluster label (SPEC §5.12): a --cluster/DEEPCOVE_CLUSTER value is written
	// through to the settings table so it persists and the UI reflects it; otherwise the stored value is
	// used, else the default.
	if cfg.Cluster != "" {
		if err := session.SaveCluster(sqlDB, session.NormalizeCluster(cfg.Cluster)); err != nil {
			return fmt.Errorf("cluster: %w", err)
		}
	}
	clusterName, err := session.LoadCluster(sqlDB)
	if err != nil {
		return fmt.Errorf("cluster: %w", err)
	}
	clusterStore := session.NewClusterStore(sqlDB, clusterName)
	logger.Info("cluster", "name", clusterStore.Name())

	// Persistence: domain-owned SessionRepository over the shared DuckDB handle. Each domain package
	// owns its own CRUD wrapper (cohesion by bounded context); `internal/db` is just the connection
	// lifecycle.
	sessionRepo := session.NewDuckDBSessionRepository(sqlDB)
	sessionSvc := session.NewService(sessionRepo, logger)
	sessionSvc.SetClusterStore(clusterStore)

	closeRuntimes, err := registerRuntimes(sessionSvc, cfg, session.NewCredentialStore(sqlDB), logger)
	if err != nil {
		return err
	}
	defer closeRuntimes()

	// Chosen once, after everything has registered, so the answer does not depend on registration order.
	if err := sessionSvc.SelectDefaultRuntime(ctx, cfg.DefaultRuntime); err != nil {
		logger.Warn("requested default runtime is unavailable on this node", "requested", cfg.DefaultRuntime, "err", err)
		if err := sessionSvc.SelectDefaultRuntime(ctx, ""); err != nil {
			return fmt.Errorf("session: %w", err)
		}
	}
	logger.Info("runtimes available", "runtimes", sessionSvc.Runtimes(), "default", sessionSvc.DefaultRuntime())

	// Bring the session table back in line with what the runtimes are actually hosting. This matters most
	// for VM sessions: their runtime's registry lived in the previous process, so every one of them is
	// gone while its row still says "running". Left alone, the session page waits forever on a bootstrap
	// probe nothing will answer. Must run AFTER the runtimes are registered, or there is nothing to ask.
	if err := sessionSvc.Reconcile(ctx); err != nil {
		logger.Warn("could not reconcile sessions with their runtimes", "err", err)
	}

	mux := http.NewServeMux()
	if err := api.New(
		api.WithConfig(cfg),
		api.WithMux(mux),
		api.WithVersion(Version),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	sessionSvc.AttachRoutes(mux)
	if err := ui.New(
		ui.WithConfig(cfg),
		ui.WithMux(mux),
		ui.WithTransport(cfg.Transport),
		ui.WithTarget(cfg.WasmTarget),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("ui: %w", err)
	}

	connectMgr := connect.NewManager(ctx, cfg.DataDir, sqlDB, mux, logger)
	connectMgr.SetClusterStore(clusterStore)
	connectMgr.AttachRoutes(mux)
	// The connect manager owns the tsnet node, so it's also the cross-node source for aggregated
	// session listing (fan-out to peers over the tailnet). No-op on a single node.
	sessionSvc.SetFederation(connectMgr)
	connectMgr.AutoStartIfEnabled()

	// Per-run token gating the local TCP front door. The SPA served on this listener receives
	// it as a cookie; runtime /api/* requires it. The CLI uses the Unix socket (filesystem perms)
	// and the tailnet uses WhoIs, so neither needs the token.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate local token: %w", err)
	}
	localToken := hex.EncodeToString(tokenBytes)

	// Local TCP: SPA + runtime API, behind LocalGate (Host allowlist + token).
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	// Timeouts: even though this listener is local (127.0.0.1) by default, no-timeout net/http
	// servers are a slowloris foot-gun and a curl --limit-rate against localhost is enough to
	// lock up a goroutine indefinitely. ReadHeaderTimeout is the load-bearing one; the others
	// bound long-lived but stalled streams.
	srv := &http.Server{
		Handler:           middleware.LocalGate(mux, localToken, middleware.LocalTokenCookieName(cfg.Addr), localAllowedHosts(cfg.Addr)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Unix socket: full runtime API for the local CLI, ungated; filesystem permissions are the
	// trust boundary.
	sockPath := socketPath(cfg.DataDir)
	sockSrv, sockLn, err := listenUnixSocket(sockPath, mux)
	if err != nil {
		return fmt.Errorf("listen unix socket %s: %w", sockPath, err)
	}
	defer func() { _ = os.Remove(sockPath) }()

	url := "http://" + cfg.Addr + "/"

	// Print the URL prominently so it's easy to copy/paste; the server runs in the foreground and
	// the browser is the primary entry point.
	fmt.Println()
	fmt.Printf("  deepcove:  %s\n", url)
	fmt.Println()

	logger.Info("server listening", "url", url, "socket", sockPath, "is_dev", cfg.IsDev)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("tcp listener: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := sockSrv.Serve(sockLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("socket listener: %w", err)
		}
		return nil
	})
	// gctx is cancelled by the shutdown signal (ctx) or by either listener failing, so both
	// servers always get drained. On a clean signal shutdown the listeners return ErrServerClosed
	// (filtered to nil above); on a drain timeout the Shutdown error surfaces. If a listener
	// failed, its error was recorded first, so errgroup keeps it as the root cause over any
	// teardown error here.
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(srv.Shutdown(shutdownCtx), sockSrv.Shutdown(shutdownCtx))
	})
	return g.Wait()
}
