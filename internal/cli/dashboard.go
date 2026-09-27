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
	"os/exec"
	"runtime"
	"time"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/ui"
)

// cmdDashboard opens the deepcove dashboard. It never starts the background service.
//
// Two cases:
//   - If a local service is already running on cfg.Addr (probed via /api/health), just open that
//     URL in the default browser and exit.
//   - Otherwise spin up a minimal HTTP server on cfg.Addr that serves only the SPA bundle, then
//     open the browser. No tsnet, no Unix socket, no DuckDB.
func cmdDashboard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := newLogger(cfg)

	url := "http://" + cfg.Addr + "/"

	// If a background service is already running, defer to it: open the browser and return.
	// We probe /api/health (which only the running service exposes; dashboard mode does not).
	if serviceRunning(ctx, cfg.Addr) {
		logger.Info("detected running deepcove service; opening browser", "url", url)
		if err := openBrowser(url); err != nil {
			logger.Warn("could not auto-open browser; copy the URL above", "err", err)
			fmt.Println()
			fmt.Printf("  deepcove:  %s\n", url)
			fmt.Println()
		}
		return nil
	}

	// Thin client mode: serve only the SPA bundle. It hosts no API of its own, so the SPA runs in
	// discover mode: the in-browser Tailscale node signs in, finds the user's deepcove nodes on the
	// tailnet, and talks to those nodes directly over WASM (no target injected here).
	mux := http.NewServeMux()
	if err := ui.New(
		ui.WithMux(mux),
		ui.DashboardMode(),
	).AttachRoutes(); err != nil {
		return fmt.Errorf("ui: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}

	// Same timeout posture as cmdStart; local listener or not, no-timeout net/http is a foot-gun.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Print the URL prominently so users can copy/paste it if the browser fails to auto-open
	// (or they're on a remote shell etc.).
	fmt.Println()
	fmt.Printf("  deepcove dashboard:  %s\n", url)
	fmt.Println()

	logger.Info("dashboard listening (thin-client mode)", "url", url)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	if err := openBrowser(url); err != nil {
		logger.Warn("could not auto-open browser; copy the URL above", "err", err)
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received; stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

// serviceRunning probes /api/health on addr with a short timeout. True means a background deepcove
// service is up and ready; false means we should start the thin-client.
func serviceRunning(ctx context.Context, addr string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+addr+"/api/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// newLogger returns a slog.Logger configured from the user's LOG_LEVEL.
func newLogger(cfg *config.Config) *slog.Logger {
	var lvl slog.Level
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	}))
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform for browser open: %s", runtime.GOOS)
	}
	return cmd.Start()
}
