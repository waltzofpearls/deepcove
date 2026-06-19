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
	dockerdrv "github.com/waltzofpearls/deepcove/internal/sandbox/docker"
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
	defer func() { _ = sqlDB.Close() }()
	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("database ready", "data_dir", cfg.DataDir)

	// Sandbox driver: Docker is the v1 backend. $HOME is the one host root sharable with sandboxes;
	// it covers the common case where the user mounts a project under their home dir (~/projects/foo,
	// ~/code, etc.). System dirs (/etc, /usr, /var, ...) stay blocked because they aren't under $HOME.
	// Power users who need to mount outside $HOME will get a config flag override as a follow-up. The
	// sandbox layer's mount-source validator enforces this.
	var allowedRoots []string
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		allowedRoots = append(allowedRoots, home)
	}
	sandbox, err := dockerdrv.New(dockerdrv.WithAllowedMountRoots(allowedRoots...))
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	defer func() { _ = sandbox.Close() }()

	// Persistence: domain-owned SessionRepository over the shared DuckDB handle. Each domain package
	// owns its own CRUD wrapper (cohesion by bounded context); `internal/db` is just the connection
	// lifecycle.
	sessionRepo := session.NewDuckDBSessionRepository(sqlDB)
	sessionSvc := session.NewService(sessionRepo, sandbox, logger)

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

	connectMgr := connect.NewManager(ctx, cfg.DataDir, sqlDB, mux)
	connectMgr.AttachRoutes(mux)
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
		Handler:           middleware.LocalGate(mux, localToken, localAllowedHosts(cfg.Addr)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Unix socket: full runtime API for the local CLI, ungated; filesystem permissions are the
	// trust boundary.
	sockPath := socketPath()
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
