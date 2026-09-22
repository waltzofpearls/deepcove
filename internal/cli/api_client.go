package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// apiClient is a thin HTTP client the CLI uses to talk to the running deepcove service. It prefers
// the local control Unix socket (filesystem-permission scoped, and not subject to the local TCP
// listener's token gate), falling back to the TCP address when the socket is absent (older server,
// or dev). It exists in this file so each command file stays small and focused.
type apiClient struct {
	baseURL string
	http    *http.Client
}

func newAPIClient(cfg *config.Config) *apiClient {
	if sock := socketPath(cfg.DataDir); socketAvailable(sock) {
		return &apiClient{
			baseURL: "http://unix", // host is ignored; the transport dials the socket
			http:    socketHTTPClient(sock),
		}
	}
	return &apiClient{
		baseURL: "http://" + cfg.Addr,
		http:    http.DefaultClient,
	}
}

// errAPI is what an /api/sessions response with a non-2xx status becomes on the client side. Wrapping
// keeps the daemon's error message visible while letting callers reason about retry/etc later.
type errAPI struct {
	Status int
	Body   string
}

func (e *errAPI) Error() string {
	return fmt.Sprintf("api %d: %s", e.Status, e.Body)
}

func (c *apiClient) Create(ctx context.Context, req session.CreateRequest) (*session.Session, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dial deepcove service at %s: %w (is `deepcove start` running?)", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return nil, readAPIError(resp)
	}
	var out session.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *apiClient) List(ctx context.Context, includeAll bool) ([]*session.Session, error) {
	// The CLI is a single-machine tool, so it lists this node's sessions (a bare array). The
	// aggregated /api/sessions (cross-node, enveloped) is the browser dashboard's endpoint.
	url := c.baseURL + "/api/sessions/local"
	if includeAll {
		url += "?all=true"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dial deepcove service at %s: %w (is `deepcove start` running?)", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return nil, readAPIError(resp)
	}
	var out []*session.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func (c *apiClient) Stop(ctx context.Context, id string) error {
	stopURL := c.baseURL + "/api/sessions/" + url.PathEscape(id) + "/stop"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("dial deepcove service at %s: %w (is `deepcove start` running?)", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return readAPIError(resp)
	}
	return nil
}

// runningSessions is what this node is running right now.
func runningSessions(ctx context.Context, client *apiClient) ([]*session.Session, error) {
	sessions, err := client.List(ctx, false)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(sessions, func(sess *session.Session) bool {
		return sess.Status != session.StatusRunning
	}), nil
}

// stopRunningSessions stops every session this node is running, and returns how many it stopped. One
// that will not stop is reported, and the rest are still stopped.
func stopRunningSessions(ctx context.Context, client *apiClient) (int, error) {
	sessions, err := runningSessions(ctx, client)
	if err != nil {
		return 0, err
	}
	var stopped int
	var stopErr error
	for _, sess := range sessions {
		if err := client.Stop(ctx, sess.ID); err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("%s: %w", sess.Name, err))
			continue
		}
		stopped++
	}
	return stopped, stopErr
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	// Try to decode the structured {"error": "..."} response; fall back to the raw body if it's not JSON.
	var asJSON map[string]string
	if json.Unmarshal(b, &asJSON) == nil {
		if msg, ok := asJSON["error"]; ok {
			return &errAPI{Status: resp.StatusCode, Body: msg}
		}
	}
	if len(b) == 0 {
		return errors.New(resp.Status)
	}
	return &errAPI{Status: resp.StatusCode, Body: string(b)}
}
