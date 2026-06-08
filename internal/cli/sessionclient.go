package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// sessionClient is a thin HTTP client the CLI uses to talk to the
// running deepcove service over its local TCP listener. It exists in
// this file so each session_*.go command stays small and focused.
//
// Once the Unix socket listener lands (SPEC §4.1.1), the dial address
// switches there — until then, talking to 127.0.0.1:8080 works for
// the single-machine case the CLI cares about.
type sessionClient struct {
	baseURL string
	http    *http.Client
}

func newSessionClient(cfg *config.Config) *sessionClient {
	return &sessionClient{
		baseURL: "http://" + cfg.Addr,
		http:    http.DefaultClient,
	}
}

// errAPI is what an /api/sessions response with a non-2xx status
// becomes on the client side. Wrapping keeps the daemon's error
// message visible while letting callers reason about retry/etc later.
type errAPI struct {
	Status int
	Body   string
}

func (e *errAPI) Error() string {
	return fmt.Sprintf("api %d: %s", e.Status, e.Body)
}

func (c *sessionClient) Create(ctx context.Context, req session.CreateRequest) (*session.Session, error) {
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

func (c *sessionClient) List(ctx context.Context, includeAll bool) ([]*session.Session, error) {
	url := c.baseURL + "/api/sessions"
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

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	// Try to decode the structured {"error": "..."} response; fall
	// back to the raw body if it's not JSON.
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
