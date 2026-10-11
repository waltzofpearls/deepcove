package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sessions send LLM requests to a proxy instead of straight to the provider. The proxy holds the
// credentials, so the sandbox holds none. Empty means sessions talk to the provider directly.
//
// Stored per node, in the settings table.

// LLMProxyStore persists the node's proxy URL. The service holds the value; this is where it survives
// a restart, in the settings table.
type LLMProxyStore interface {
	Load() (string, error)
	Save(url string) error
}

type duckdbLLMProxyStore struct{ db *sql.DB }

// NewDuckDBLLMProxyStore constructs an LLMProxyStore over the given database.
func NewDuckDBLLMProxyStore(database *sql.DB) LLMProxyStore {
	return &duckdbLLMProxyStore{db: database}
}

func (s *duckdbLLMProxyStore) Load() (string, error) { return LoadLLMProxyURL(s.db) }
func (s *duckdbLLMProxyStore) Save(url string) error { return SaveLLMProxyURL(s.db, url) }

// SetLLMProxyStore wires persistence, so the API can change the proxy rather than only the flag.
func (s *Service) SetLLMProxyStore(store LLMProxyStore) { s.llmProxyStore = store }

// LLMProxyURL is where sessions on this node send LLM requests, or empty for the provider.
func (s *Service) LLMProxyURL() string {
	s.llmProxyMu.RLock()
	defer s.llmProxyMu.RUnlock()
	return s.llmProxyURL
}

// SaveLLMProxy normalizes, stores and applies a proxy URL. Empty clears it.
//
// It applies to machines already running: they ask for this address on every request. A session still
// has to be told to use it, which happens when its agent launches.
func (s *Service) SaveLLMProxy(rawURL string) (string, error) {
	if s.llmProxyStore == nil {
		return "", fmt.Errorf("session: this node cannot store an llm proxy url")
	}
	normalized, err := NormalizeLLMProxyURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if err := s.llmProxyStore.Save(normalized); err != nil {
		return "", err
	}
	s.setLLMProxyURL(normalized)
	return normalized, nil
}

// SetLLMProxyURL points every session on this node at a proxy. Empty sends them to the provider.
func (s *Service) SetLLMProxyURL(rawURL string) { s.setLLMProxyURL(rawURL) }

func (s *Service) setLLMProxyURL(rawURL string) {
	s.llmProxyMu.Lock()
	defer s.llmProxyMu.Unlock()
	s.llmProxyURL = rawURL
}

// SetTailnetClient wires the tailnet client, the same one the VM runtime forwards with.
func (s *Service) SetTailnetClient(client func() *http.Client) { s.tailnetClient = client }

// TestLLMProxy makes one request to the configured address and reports what happened, so the UI can say
// whether it is reachable instead of leaving you to find out inside a session.
func (s *Service) TestLLMProxy(ctx context.Context) (string, error) {
	raw := s.LLMProxyURL()
	if raw == "" {
		return "", fmt.Errorf("%w: no llm proxy is configured", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.proxyClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", raw, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Any answer proves reachability. A proxy is entitled to refuse a bare GET, and often does.
	return fmt.Sprintf("%s answered with %s", raw, resp.Status), nil
}

// proxyClient reaches the address the same way a session's requests will: over the tailnet when this
// node has signed in, and over its ordinary network when it has not.
func (s *Service) proxyClient() *http.Client {
	if s.tailnetClient != nil {
		if c := s.tailnetClient(); c != nil {
			return c
		}
	}
	return http.DefaultClient
}

// SaveLLMProxyURL stores the URL every session on this node uses. Empty clears it.
func SaveLLMProxyURL(db *sql.DB, rawURL string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES ('llm_proxy_url', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		rawURL,
	)
	return err
}

// LoadLLMProxyURL reads the stored URL, or "" when sessions should use the provider.
func LoadLLMProxyURL(db *sql.DB) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = 'llm_proxy_url'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// NormalizeLLMProxyURL keeps the scheme and host and rejects the rest.
//
// Plain http is expected: the hop runs over the tailnet, which is already encrypted. A path or query
// is rejected rather than quietly dropped, because agents append their own paths to this and would
// break in a way that is hard to trace back here.
func NormalizeLLMProxyURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("session: proxy url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("session: proxy url %q needs an http or https scheme", rawURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("session: proxy url %q has no host", rawURL)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("session: proxy url %q must be a scheme and host only", rawURL)
	}
	if u.User != nil {
		return "", fmt.Errorf("session: proxy url %q must not carry credentials", rawURL)
	}
	return u.Scheme + "://" + u.Host, nil
}
