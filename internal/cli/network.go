package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// networkPollInterval is how often the login command asks whether the sign-in finished. The wait is
// measured in minutes of human time, so asking faster only burns requests.
const networkPollInterval = 2 * time.Second

// cmdNetwork is the router for `deepcove network ...`.
//
// Usage:
//
//	deepcove network login [--timeout 10m] [--force-reauth]
//	deepcove network status
//	deepcove network logout
//	deepcove network disconnect
//
// Every verb talks to the running node, because the tailnet connection lives in that process. None of
// them work before `deepcove start` or `deepcove service install`.
func cmdNetwork(ctx context.Context, args []string) error {
	if len(args) == 0 {
		networkUsage(os.Stderr)
		return errors.New("network needs a verb (e.g. `deepcove network login`)")
	}
	switch args[0] {
	case "-h", "--help", "help":
		networkUsage(os.Stdout)
		return nil
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	client := newAPIClient(cfg)

	switch args[0] {
	case "login":
		return cmdNetworkLogin(ctx, client, args[1:])
	case "status":
		status, err := client.NetworkStatus(ctx)
		if err != nil {
			return err
		}
		printNetworkStatus(status)
		return nil
	case "logout":
		status, err := client.networkRequest(ctx, http.MethodPost, "/api/connect/reset", nil)
		if err != nil {
			return err
		}
		fmt.Println("signed out, this machine is no longer on your Tailscale network")
		printNetworkStatus(status)
		return nil
	case "disconnect":
		status, err := client.networkRequest(ctx, http.MethodPost, "/api/connect/disconnect", nil)
		if err != nil {
			return err
		}
		fmt.Println("disconnected, `deepcove network login` reconnects without signing in again")
		printNetworkStatus(status)
		return nil
	}
	networkUsage(os.Stderr)
	return fmt.Errorf("unknown network verb %q", args[0])
}

// cmdNetworkLogin asks the node to connect, shows the sign-in URL when one is needed, and waits until
// the node is on the tailnet.
func cmdNetworkLogin(ctx context.Context, client *apiClient, args []string) error {
	fs := flag.NewFlagSet("network login", flag.ContinueOnError)
	// Long enough to walk to another machine and finish in a browser, short enough that a forgotten
	// terminal returns on its own instead of waiting for ever.
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for the sign-in to finish")
	authKey := fs.String("auth-key", "", "enrol with a Tailscale auth key instead of signing in")
	if err := fs.Parse(args); err != nil {
		return err
	}

	status, err := client.connectNode(ctx, *authKey)
	if err != nil {
		return err
	}
	if status.Phase == string(tailnet.PhaseRunning) {
		fmt.Println("already on your Tailscale network")
		printNetworkStatus(status)
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	status, err = client.waitForNetwork(ctx, status, networkPollInterval, printLoginURL)
	if err != nil {
		return err
	}
	fmt.Println("connected")
	printNetworkStatus(status)
	return nil
}

// waitForNetwork polls until the node is on the tailnet, calling onLoginURL the first time there is a
// URL to show. It stops on ctx, so both Ctrl-C and the timeout end the wait without stopping the node,
// which keeps trying on its own.
func (c *apiClient) waitForNetwork(
	ctx context.Context, status *connect.Status, every time.Duration, onLoginURL func(string),
) (*connect.Status, error) {
	shown := false
	for {
		if status.LoginURL != "" && !shown {
			shown = true
			onLoginURL(status.LoginURL)
		}
		if status.Phase == string(tailnet.PhaseRunning) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("stopped waiting for the sign-in: %w", ctx.Err())
		case <-time.After(every):
		}
		next, err := c.NetworkStatus(ctx)
		if err != nil {
			// The deadline can land mid-request, so report the wait ending rather than a dial failure.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("stopped waiting for the sign-in: %w", ctx.Err())
			}
			return nil, err
		}
		status = next
	}
}

// connectNode asks the node to connect, carrying an auth key when there is one. The key rides the
// local socket rather than a command line, so it never lands in a service definition or in `ps`.
func (c *apiClient) connectNode(ctx context.Context, authKey string) (*connect.Status, error) {
	if authKey == "" {
		return c.networkRequest(ctx, http.MethodPost, "/api/connect", nil)
	}
	body, err := json.Marshal(map[string]string{"auth_key": authKey})
	if err != nil {
		return nil, err
	}
	return c.networkRequest(ctx, http.MethodPost, "/api/connect", bytes.NewReader(body))
}

// NetworkStatus reports what the node thinks its tailnet connection is doing.
func (c *apiClient) NetworkStatus(ctx context.Context) (*connect.Status, error) {
	return c.networkRequest(ctx, http.MethodGet, "/api/connect/status", nil)
}

func (c *apiClient) networkRequest(
	ctx context.Context, method, path string, body io.Reader,
) (*connect.Status, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dial deepcove service at %s: %w (is `deepcove start` running?)", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, readAPIError(resp)
	}
	var out connect.Status
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func printLoginURL(url string) {
	lines := []string{
		"",
		"To put this machine on your Tailscale network, open:",
		"",
		"  " + url,
		"",
		"Waiting for you to finish. Ctrl-C stops waiting; the node keeps trying on its own.",
	}
	_, _ = fmt.Print(joinStringLines(lines))
	// Best effort, and deliberately quiet: a headless machine has no browser to open, which is the
	// case this command exists for. The URL is printed above either way.
	_ = openBrowser(url)
}

func printNetworkStatus(status *connect.Status) {
	fmt.Printf("state:   %s\n", status.Phase)
	if status.Name != "" {
		fmt.Printf("name:    %s\n", status.Name)
	}
	if status.Tailnet != "" {
		fmt.Printf("tailnet: %s\n", status.Tailnet)
	}
	if len(status.IPs) > 0 {
		fmt.Printf("address: %s\n", strings.Join(status.IPs, ", "))
	}
	if status.URL != "" {
		fmt.Printf("url:     %s\n", status.URL)
	}
}

func networkUsage(w *os.File) {
	lines := []string{
		"deepcove network: put this machine on your Tailscale network",
		"",
		"Usage:",
		"  deepcove network login [--timeout 10m] [--auth-key tskey-...] [--force-reauth]",
		"  deepcove network status",
		"  deepcove network logout      sign out, this machine leaves your network",
		"  deepcove network disconnect  stop for now, without signing out",
		"",
		"The node has to be running already: `deepcove start`, or `deepcove service install`.",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
