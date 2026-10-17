package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api"
	"github.com/waltzofpearls/deepcove/internal/api/middleware"
	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
	"github.com/waltzofpearls/deepcove/internal/ui"
	"golang.org/x/sync/errgroup"
)

// cmdStart runs the deepcove background service. Default for solo and most users.
//
// Mounts the three front doors over one mux:
//   - local TCP on cfg.Addr: SPA + runtime API, behind LocalGate (same OS user + Host allowlist)
//   - Unix socket: full runtime API for the local CLI (filesystem perms)
//   - tailnet (via connect.Manager): owner-gated, when connected
//
// Backed by DuckDB + migrations, the sandbox runtimes, and the session lifecycle service.
func cmdStart(ctx context.Context, args []string) error {
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "address to listen on; keep the 127.0.0.1 prefix unless you mean to be reachable")
	// Persisted, so it survives a restart and the UI can edit it later.
	llmProxyFlag := fs.String("llm-proxy-url", "",
		"send every session's LLM requests here instead of to the provider, e.g. http://100.75.35.244:9001 (persisted)")
	// Repeatable, and the only way to open a hole in the sandbox network block. Not an env var and not
	// a setting: a build nobody passed this to cannot have one.
	var allowSandboxHosts repeatedFlag
	fs.Var(&allowSandboxHosts, "sandbox-can-reach",
		"IP or CIDR a sandbox may reach on a private network (repeatable, or comma separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := allowSandboxHosts.validateAddrs(runtime.GOOS); err != nil {
		return fmt.Errorf("sandbox-can-reach: %w", err)
	}
	cfg.AllowSandboxHosts = allowSandboxHosts

	logger := newLogger(cfg)
	// Tailscale's own logging is not slog, so the level has to be handed to it explicitly.
	tailnet.SetVerbose(strings.EqualFold(cfg.LogLevel, "debug"))
	if len(cfg.AllowSandboxHosts) > 0 {
		// Loud on purpose. Every session on this node gets these holes, so it should never be a surprise.
		logger.Warn("sandboxes on this node can reach these private addresses",
			"hosts", cfg.AllowSandboxHosts)
	}

	sqlDB, err := db.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := db.Migrate(sqlDB); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if open, err := config.EnvFileOpenToOthers(cfg.DataDir); err == nil && open {
		logger.Warn("other users on this machine can read your stored settings",
			"path", config.EnvFilePath(cfg.DataDir),
			"fix", fmt.Sprintf("chmod 600 %s", config.EnvFilePath(cfg.DataDir)))
	}
	if err := db.VerifyIndexes(ctx, sqlDB); err != nil {
		logger.Error("the database is not answering correctly, so sessions may be missing",
			"err", err, "fix", "stop deepcove and run: deepcove db repair")
	}
	logger.Info("database ready", "data_dir", cfg.DataDir)

	// Persistence: domain-owned SessionRepository over the shared DuckDB connection. Each domain package
	// owns its own CRUD wrapper (cohesion by bounded context); `internal/db` is just the connection
	// lifecycle.
	// Where sessions send their LLM requests. An explicit --llm-proxy-url is stored, so the setting
	// outlives the flag and a later restart needs no argument.
	if *llmProxyFlag != "" {
		normalized, err := session.NormalizeLLMProxyURL(*llmProxyFlag)
		if err != nil {
			return err
		}
		if err := session.SaveLLMProxyURL(sqlDB, normalized); err != nil {
			return fmt.Errorf("llm proxy url: %w", err)
		}
	}
	llmProxyURL, err := session.LoadLLMProxyURL(sqlDB)
	if err != nil {
		return fmt.Errorf("llm proxy url: %w", err)
	}

	sessionRepo := session.NewDuckDBSessionRepository(sqlDB)
	sessionSvc := session.NewService(sessionRepo, logger)
	sessionSvc.SetWorkspaceVMStore(session.NewDuckDBWorkspaceVMStore(sqlDB))
	sessionSvc.SetDataDir(cfg.DataDir)
	sessionSvc.SetLLMProxyStore(session.NewDuckDBLLMProxyStore(sqlDB))
	sessionSvc.SetLLMProxyURL(llmProxyURL)
	if llmProxyURL != "" {
		logger.Info("sessions send LLM requests through a proxy", "url", llmProxyURL)
	}

	// Assigned below, once the connect manager exists. Only read when a VM starts, long after.
	var connectMgr *connect.Manager
	tailnetClient := func() *http.Client {
		if connectMgr == nil {
			return nil
		}
		return connectMgr.TailnetClient()
	}
	sessionSvc.SetTailnetClient(tailnetClient)

	// The user's own agent logins, for the sessions that share them. Only this node refreshes them.
	sharedLoginStore := session.NewSharedLoginStore(sqlDB)
	logins := sharedlogin.NewKeeper(sharedLoginStore, sharedlogin.Providers(&http.Client{Timeout: 30 * time.Second}))
	sessionSvc.SetSharedLogins(logins, sharedLoginStore)
	runtimes := registerRuntimes(sessionSvc, cfg, logins, logger, tailnetClient)
	defer runtimes.close()
	// Defers unwind in reverse: the lifetime ends first, on every return and not only a shutdown signal,
	// then the restarts on it are waited for, then the runtimes close.
	lifetime, endLifetime := context.WithCancel(ctx)
	defer sessionSvc.WaitForRestarts()
	defer endLifetime()
	sessionSvc.SetLifetime(lifetime)
	// Defers unwind in reverse, so this runs before the runtimes and the database close. Terminal
	// streams survive the HTTP drain (their connection is hijacked) and need both to clean up.
	defer sessionSvc.CloseAttachStreams(attachCloseGrace)

	// Chosen after everything has registered, so the answer does not depend on registration order, and
	// again when a runtime is installed later.
	if err := sessionSvc.SelectDefaultRuntime(ctx, cfg.DefaultRuntime); err != nil {
		logger.Warn("requested default runtime is not provided on this node; picking the best default it has, which may be none", "requested", cfg.DefaultRuntime, "err", err)
	}
	logger.Info("runtimes available", "runtimes", sessionSvc.Runtimes(), "default", sessionSvc.DefaultRuntime())

	// Bring the session table back in line with what the runtimes are actually hosting, then restart what
	// was running. VM sessions died with the previous process, so without this they all sit stopped.
	// Must run AFTER the runtimes are registered, or there is nothing to ask.
	if lost := sessionSvc.ReconcileSessions(ctx); len(lost) > 0 {
		// In the background: each VM takes seconds to boot, and the node should serve requests meanwhile.
		sessionSvc.RestartSessionsInBackground(lost)
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
		ui.WithMux(mux),
		ui.WithTransport(cfg.Transport),
		ui.WithTarget(cfg.WasmTarget),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("ui: %w", err)
	}

	connectMgr = connect.NewManager(ctx, cfg.DataDir, sqlDB, mux, logger)
	connectMgr.AttachRoutes(mux)
	// The connect manager owns the tsnet node, so it's also the cross-node source for aggregated
	// session listing (fan-out to peers over the tailnet). No-op on a single node.
	sessionSvc.SetFederation(connectMgr)
	connectMgr.RegisterGossipTopic(session.GossipTopicSessionHosts, func() (string, bool) {
		ids, answered := sessionSvc.LocalSessionIDs(ctx)
		return strings.Join(ids, ","), answered
	})
	connectMgr.AutoStartIfEnabled()

	// Local TCP: SPA + runtime API, behind LocalGate, which answers only the user who runs deepcove.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	// Timeouts: even though this listener is local (127.0.0.1) by default, no-timeout net/http
	// servers are a slowloris foot-gun and a curl --limit-rate against localhost is enough to
	// lock up a goroutine indefinitely. ReadHeaderTimeout is the load-bearing one; the others
	// bound long-lived but stalled streams.
	srv := &http.Server{
		Handler:           middleware.LocalGate(mux, localAllowedHosts(cfg.Addr)),
		ConnContext:       middleware.LocalConnContext,
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

	logger.Info("server listening", "url", url, "socket", sockPath)

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
	// gctx is cancelled by the shutdown signal (ctx) or by either listener failing, so both servers
	// always get drained. If a listener failed, its error was recorded first, so errgroup keeps it as
	// the root cause over any teardown error here.
	g.Go(func() error {
		<-gctx.Done()
		watchShutdown(cfg.DataDir, logger, runtimes)
		return drainServers(shutdownGrace, logger, srv, sockSrv)
	})
	return g.Wait()
}

// shutdownGrace is how long in flight requests get to finish when stopping.
const shutdownGrace = 2 * time.Second

// shutdownHardStop bounds the whole teardown. Past this, something is stuck and waiting longer only
// risks reaching the state where the kernel holds a thread and no signal can reap the process.
const shutdownHardStop = 20 * time.Second

// attachCloseGrace bounds the wait for terminal streams to clean up: a tmux kill plus a last activity
// write, both of which have their own shorter timeouts.
const attachCloseGrace = 5 * time.Second

// drainServers stops servers, then closes anything still connected.
//
// The close matters: net/http treats a connection that has not sent a request yet as busy for its first
// 5 seconds, and browsers keep spare connections open, so a graceful drain alone times out and makes a
// normal stop look like it failed. Nothing is in flight on those, so closing them loses no work.
func drainServers(grace time.Duration, logger *slog.Logger, servers ...*http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	errs := make([]error, 0, len(servers))
	for _, srv := range servers {
		errs = append(errs, srv.Shutdown(ctx))
	}
	err := errors.Join(errs...)
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	logger.Info("closing remaining connections after the drain window")
	errs = errs[:0]
	for _, srv := range servers {
		errs = append(errs, srv.Close())
	}
	return errors.Join(errs...)
}

// forceCloseGrace bounds the last attempt to put the VMs down.
const forceCloseGrace = 3 * time.Second

// watchShutdown force-exits if teardown wedges, after writing every goroutine stack to a file. The stacks
// are what explain the wedge.
//
// The VMs are stopped before exiting, so none is left running with nobody to stop it.
func watchShutdown(dataDir string, logger *slog.Logger, runtimes *runtimeSet) {
	go func() {
		time.Sleep(shutdownHardStop)
		path, err := dumpStacks(dataDir)
		if err != nil {
			logger.Error("shutdown is stuck, and the stacks could not be written", "err", err)
		} else {
			// Printed, not just logged: a stuck node floods the terminal, and this is the one line
			// worth finding in it.
			_, _ = fmt.Fprintf(os.Stderr, "\n\n  shutdown is stuck. goroutine stacks written to:\n  %s\n\n", path)
			logger.Error("shutdown is stuck, forcing exit", "stacks", path)
		}
		forceCloseRuntimes(logger, runtimes)
		os.Exit(1)
	}()
}

func forceCloseRuntimes(logger *slog.Logger, runtimes *runtimeSet) {
	done := make(chan error, 1)
	go func() { done <- runtimes.forceClose() }()
	select {
	case err := <-done:
		if err != nil {
			logger.Error("could not put every vm down before exiting", "err", err)
		}
	case <-time.After(forceCloseGrace):
		logger.Error("the hypervisor did not put the vms down either, exiting anyway")
	}
}

// dumpStacksMaxBytes caps the dump. A process with a runaway goroutine count is already in trouble, and
// writing an unbounded file is not how it gets better.
const dumpStacksMaxBytes = 64 << 20

// dumpStacks writes every goroutine stack to a file and returns its path. A file, not stderr: a wedged
// node prints faster than a terminal keeps, so the one thing worth reading would scroll away.
func dumpStacks(dataDir string) (string, error) {
	stacks, truncated := goroutineStacks()
	if truncated {
		stacks = append(stacks, "\n[truncated: the dump reached the cap]\n"...)
	}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "shutdown-stacks.txt")
	if err := os.WriteFile(path, stacks, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// goroutineStacks returns every goroutine stack, growing the buffer until the dump fits or the cap is
// reached. A fixed buffer cuts the dump off silently, and the goroutine that explains a wedge is as
// likely to be at the end as the start.
func goroutineStacks() (stacks []byte, truncated bool) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n], false
		}
		if len(buf) >= dumpStacksMaxBytes {
			return buf[:n], true
		}
		buf = make([]byte, min(2*len(buf), dumpStacksMaxBytes))
	}
}
