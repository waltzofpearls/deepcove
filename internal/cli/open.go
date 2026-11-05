package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api/middleware"
	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/service"
)

// unlockCodePath is where `deepcove open` asks for an unlock link. Only the Unix socket serves it.
const unlockCodePath = "/unlock-code"

// Where `deepcove open` asks for a link to open in the default browser, or for one to paste by hand.
const (
	askLinkToOpen  = unlockCodePath
	askLinkToPaste = unlockCodePath + "?" + middleware.PasteQuery
)

// serviceStartWait is how long `deepcove open` waits for a service that is still starting, which is what
// the installer runs into right after it starts one.
const serviceStartWait = 10 * time.Second

// unlockAskTimeout bounds each ask, so a deepcove that takes the call and never answers cannot hang
// `deepcove open` or the installer.
const unlockAskTimeout = 5 * time.Second

// webNodeURL is the hosted web node, which reaches deepcove on every machine in your tailnet.
const webNodeURL = "https://app.deepcove.dev"

var errNotRunning = errors.New("deepcove is not running")

// cmdOpen opens deepcove in the default browser, unlocked. It never starts deepcove.
//
// It asks the running deepcove for an unlock link over the Unix socket and opens the browser on it. The
// link holds a code that works once, for a few seconds, and the browser trades it for the cookie. With
// --no-browser it prints the link instead, for a browser that is not the default one.
func cmdOpen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	noBrowser := fs.Bool("no-browser", false, "print an unlock link to paste into any browser, instead of opening the default one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	codePath := askLinkToOpen
	if *noBrowser {
		codePath = askLinkToPaste
	}
	status := readServiceStatus(ctx)
	unlockURL, err := askUnlockURLWhileStarting(ctx, cfg, codePath, status.Running)
	switch {
	case err == nil && *noBrowser:
		fmt.Printf("Paste this link into the browser you want to unlock, within %d seconds:\n  %s\n",
			int(middleware.PastedUnlockCodeLife.Seconds()), unlockURL)
		return nil
	case err == nil:
		if err := openBrowser(unlockURL); err != nil {
			fmt.Printf("Couldn't open your browser. Open this link within %d seconds:\n  %s\n",
				int(middleware.UnlockCodeLife.Seconds()), unlockURL)
			return nil
		}
		fmt.Println("deepcove is open in your browser.")
		// A launcher that starts can still fail to show anything, which only it would know.
		fmt.Println("Not open? Run `deepcove open --no-browser` and paste the link it prints.")
		return nil
	case !errors.Is(err, errNotRunning):
		return err
	case status.Running:
		return errors.New("running in the background but did not answer. `deepcove service status` shows where its logs are")
	}
	return buildNotRunningErrorFrom(status)
}

// buildNotRunningErrorFrom says how to start deepcove here, and where deepcove on other machines is.
func buildNotRunningErrorFrom(status service.Status) error {
	startCommand := findStartCommand()
	if status.Installed {
		startCommand = "deepcove service start"
	}
	return fmt.Errorf("not running here. Start it with:\n  %s\n"+
		"To reach deepcove on your other machines, open %s", startCommand, webNodeURL)
}

// findStartCommand is what people should run to start deepcove: the background service, which keeps
// running after the terminal closes. Only a platform with no service yet gets deepcove in a terminal.
func findStartCommand() string {
	if service.IsSupported() {
		return "deepcove service install"
	}
	return "deepcove start"
}

// askUnlockURLWhileStarting asks the running deepcove for an unlock link. A background service gets a
// few seconds to open its socket, in case it is still starting.
func askUnlockURLWhileStarting(ctx context.Context, cfg *config.Config, codePath string, inBackground bool) (string, error) {
	deadline := time.Now()
	if inBackground {
		deadline = deadline.Add(serviceStartWait)
	}
	for {
		unlockURL, err := askUnlockURL(ctx, socketPath(cfg.DataDir), codePath, unlockAskTimeout)
		if !errors.Is(err, errNotRunning) || time.Now().After(deadline) {
			return unlockURL, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func askUnlockURL(ctx context.Context, sock, codePath string, timeout time.Duration) (string, error) {
	if !socketAvailable(sock) {
		return "", errNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+codePath, nil)
	if err != nil {
		return "", err
	}
	response, err := socketHTTPClient(sock).Do(request)
	if errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("did not answer within %s. "+
			"`deepcove service status` shows where its logs are", timeout)
	}
	if err != nil {
		// A socket left behind by a deepcove that crashed refuses the dial.
		return "", fmt.Errorf("%w: %v", errNotRunning, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", readAPIError(response)
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode unlock link: %w", err)
	}
	return body.URL, nil
}

// readServiceStatus is the zero status when this platform has no service or it cannot be asked.
func readServiceStatus(ctx context.Context) service.Status {
	manager, err := newServiceManager(ctx)
	if err != nil {
		return service.Status{}
	}
	status, err := manager.Status()
	if err != nil {
		return service.Status{}
	}
	return status
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
