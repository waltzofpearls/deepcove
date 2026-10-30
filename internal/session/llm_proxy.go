package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
)

// Sessions send LLM requests to a proxy instead of straight to the provider. The proxy holds the
// credentials, so the sandbox holds none. Empty means sessions talk to the provider directly.
//
// Stored per node, in the settings table.

// LLMProxyStore persists the node's proxy URL and its headers. The service holds the values; this is
// where they survive a restart, in the settings table.
type LLMProxyStore interface {
	Load() (llmproxy.Proxy, error)
	Save(proxy llmproxy.Proxy) error
}

type duckdbLLMProxyStore struct{ db *sql.DB }

// NewDuckDBLLMProxyStore constructs an LLMProxyStore over the given database.
func NewDuckDBLLMProxyStore(database *sql.DB) LLMProxyStore {
	return &duckdbLLMProxyStore{db: database}
}

func (s *duckdbLLMProxyStore) Load() (llmproxy.Proxy, error) {
	return LoadLLMProxy(s.db)
}

func (s *duckdbLLMProxyStore) Save(proxy llmproxy.Proxy) error {
	return StoreLLMProxy(s.db, proxy)
}

// SetLLMProxyStore wires persistence, so the API can change the proxy rather than only the flag.
func (s *Service) SetLLMProxyStore(store LLMProxyStore) { s.llmProxyStore = store }

// LLMProxyURL is where sessions on this node send LLM requests, or empty for the provider.
func (s *Service) LLMProxyURL() string {
	s.llmProxyMu.RLock()
	defer s.llmProxyMu.RUnlock()
	return s.llmProxyURL
}

// LLMProxy is the URL and the headers together, read under one lock so a request never pairs one
// proxy's URL with another's headers. The headers are a copy, safe to hand to a request.
func (s *Service) LLMProxy() llmproxy.Proxy {
	s.llmProxyMu.RLock()
	defer s.llmProxyMu.RUnlock()
	return llmproxy.Proxy{
		URL:     s.llmProxyURL,
		Headers: s.llmProxyHeaders.Clone(),
	}
}

// LLMProxyHeaderNames lists the headers that are set, sorted. Values never leave the node.
func (s *Service) LLMProxyHeaderNames() []string {
	return sortedHeaderNames(s.LLMProxy().Headers)
}

func sortedHeaderNames(headers http.Header) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// SetLLMProxyHeaders applies headers loaded at startup.
func (s *Service) SetLLMProxyHeaders(headers http.Header) {
	s.llmProxyMu.Lock()
	defer s.llmProxyMu.Unlock()
	s.llmProxyHeaders = headers
}

// LLMProxyHeaderEdit is one header in a save. A nil Value keeps the value already saved, which the UI
// never sees.
type LLMProxyHeaderEdit struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

// LLMProxyHeaderError names the header a save was refused for, so the UI can show it on that row.
type LLMProxyHeaderError struct {
	Name   string
	Reason string
}

func (e LLMProxyHeaderError) Error() string {
	return e.Name + ": " + e.Reason
}

// SaveLLMProxy stores and applies the URL and the headers together. The headers are the full list:
// one left out is removed. An empty URL sends sessions to the provider.
//
// It applies to machines already running: they ask for the proxy on every request. A session still
// has to be told to use it, which happens when its agent launches.
func (s *Service) SaveLLMProxy(rawURL string, edits []LLMProxyHeaderEdit) error {
	if s.llmProxyStore == nil {
		return fmt.Errorf("session: this node cannot store an llm proxy")
	}
	s.llmProxyMu.Lock()
	defer s.llmProxyMu.Unlock()
	saved := llmproxy.Proxy{
		URL:     s.llmProxyURL,
		Headers: s.llmProxyHeaders,
	}
	proxy, err := applyLLMProxyEdits(saved, rawURL, edits)
	if err != nil {
		return err
	}
	if err := s.llmProxyStore.Save(proxy); err != nil {
		return err
	}
	s.llmProxyURL = proxy.URL
	s.llmProxyHeaders = proxy.Headers
	return nil
}

// applyLLMProxyEdits builds the proxy a save asks for from the one saved now.
func applyLLMProxyEdits(saved llmproxy.Proxy, rawURL string, edits []LLMProxyHeaderEdit) (llmproxy.Proxy, error) {
	// No list at all, not an empty one, comes from a UI older than headers. Read as an empty list, its
	// save would delete every header this node has.
	if edits == nil {
		proxy, err := LLMProxyWithURL(saved, rawURL)
		if err != nil {
			return llmproxy.Proxy{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
		}
		return proxy, nil
	}
	normalized, err := NormalizeLLMProxyURL(rawURL)
	if err != nil {
		return llmproxy.Proxy{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	// A saved value is usually a token for the saved host. Keeping it would send that token to the new one.
	movedHost := saved.URL != "" && !sameLLMProxyHost(saved.URL, normalized)
	headers := http.Header{}
	for _, edit := range edits {
		name, value, err := editedLLMProxyHeader(saved.Headers, edit, movedHost)
		if err != nil {
			return llmproxy.Proxy{}, err
		}
		if headers.Get(name) != "" {
			return llmproxy.Proxy{}, LLMProxyHeaderError{
				Name:   name,
				Reason: "This header is in the list twice.",
			}
		}
		headers.Set(name, value)
	}
	if normalized == "" && len(headers) > 0 {
		return llmproxy.Proxy{}, fmt.Errorf("%w: headers need a URL to go to. Enter one, or remove the headers", ErrInvalidInput)
	}
	return llmproxy.Proxy{
		URL:     normalized,
		Headers: headers,
	}, nil
}

// editedLLMProxyHeader is the name and value one edit leaves, a new value or the saved one.
func editedLLMProxyHeader(saved http.Header, edit LLMProxyHeaderEdit, movedHost bool) (name, value string, err error) {
	if edit.Value != nil {
		name, value, err = NormalizeLLMProxyHeader(edit.Name, *edit.Value)
		if err != nil {
			return "", "", LLMProxyHeaderError{
				Name:   strings.TrimSpace(edit.Name),
				Reason: err.Error(),
			}
		}
		return name, value, nil
	}
	name = http.CanonicalHeaderKey(strings.TrimSpace(edit.Name))
	value = saved.Get(name)
	if value == "" {
		return "", "", LLMProxyHeaderError{
			Name:   name,
			Reason: "This header has no saved value. Enter one.",
		}
	}
	if movedHost {
		return "", "", LLMProxyHeaderError{
			Name:   name,
			Reason: "The URL points at a new host. Enter this value again, or remove the header.",
		}
	}
	return name, value, nil
}

// sameLLMProxyHost compares host names only, so a new port or scheme on the same host keeps the headers.
func sameLLMProxyHost(oldURL, newURL string) bool {
	oldParsed, oldErr := url.Parse(oldURL)
	newParsed, newErr := url.Parse(newURL)
	if oldErr != nil || newErr != nil {
		return false
	}
	return strings.EqualFold(oldParsed.Hostname(), newParsed.Hostname())
}

// LLMProxyWithURL replaces the saved URL for a caller that sends no headers, such as --llm-proxy-url. The
// saved headers are kept only when the host is the same.
func LLMProxyWithURL(saved llmproxy.Proxy, rawURL string) (llmproxy.Proxy, error) {
	normalized, err := NormalizeLLMProxyURL(rawURL)
	if err != nil {
		return llmproxy.Proxy{}, err
	}
	proxy := llmproxy.Proxy{
		URL:     normalized,
		Headers: saved.Headers,
	}
	if saved.URL != "" && !sameLLMProxyHost(saved.URL, normalized) {
		proxy.Headers = http.Header{}
	}
	return proxy, nil
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

// LLMProxyTestVerdict is what a test found. The UI shows it as an icon next to the result.
type LLMProxyTestVerdict string

const (
	LLMProxyReached     LLMProxyTestVerdict = "reached"
	LLMProxyRejected    LLMProxyTestVerdict = "rejected"
	LLMProxyUnreachable LLMProxyTestVerdict = "unreachable"
)

// LLMProxyTestResult is the verdict and what happened, in words.
type LLMProxyTestResult struct {
	Verdict LLMProxyTestVerdict `json:"verdict"`
	Result  string              `json:"result"`
}

// TestLLMProxy makes one request with the values in the form, saved or not, and reports what happened.
// So the UI can say whether the proxy is reachable before you save, instead of leaving you to find out
// inside a session.
func (s *Service) TestLLMProxy(ctx context.Context, rawURL string, edits []LLMProxyHeaderEdit) (LLMProxyTestResult, error) {
	proxy, err := applyLLMProxyEdits(s.LLMProxy(), rawURL, edits)
	if err != nil {
		return LLMProxyTestResult{}, err
	}
	raw := proxy.URL
	if raw == "" {
		return LLMProxyTestResult{}, fmt.Errorf("%w: enter a URL to test", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return LLMProxyTestResult{}, err
	}
	for name, values := range proxy.Headers {
		req.Header[name] = values
	}
	// A client follows redirects and carries a custom header like a token to whatever host one names.
	// The first answer is all a reachability check needs.
	client := *s.proxyClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return LLMProxyTestResult{
			Verdict: LLMProxyUnreachable,
			Result:  fmt.Sprintf("could not reach %s: %v", raw, err),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	result := LLMProxyTestResult{
		Verdict: llmProxyTestVerdict(raw, resp),
		Result:  fmt.Sprintf("%s answered with %s", raw, resp.Status),
	}
	// Where a redirect points tells a proxy's own page apart from a gateway's login page.
	if location, err := resp.Location(); err == nil {
		location.RawQuery = ""
		location.Fragment = ""
		result.Result += fmt.Sprintf(", pointing to %s", location)
	}
	return result, nil
}

// llmProxyTestVerdict reads one answer. Any answer proves the proxy is reachable, and many refuse a
// bare GET with a 404 or 405. A 401, 403 or 407, or a redirect to another host, which is usually a
// login page, means a gateway in front probably refused the headers.
func llmProxyTestVerdict(proxyURL string, resp *http.Response) LLMProxyTestVerdict {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired:
		return LLMProxyRejected
	}
	location, err := resp.Location()
	if err == nil && !sameLLMProxyHost(proxyURL, location.String()) {
		return LLMProxyRejected
	}
	return LLMProxyReached
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

// StoreLLMProxy writes the URL and the headers in one transaction, so the node never restarts with one
// host's URL and another host's headers.
func StoreLLMProxy(db *sql.DB, proxy llmproxy.Proxy) error {
	values := make(map[string]string, len(proxy.Headers))
	for name := range proxy.Headers {
		values[name] = proxy.Headers.Get(name)
	}
	headers, err := json.Marshal(values)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	settings := map[string]string{
		"llm_proxy_url":     proxy.URL,
		"llm_proxy_headers": string(headers),
	}
	for key, value := range settings {
		_, err := tx.Exec(
			`INSERT INTO settings (key, value) VALUES (?, ?)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			key, value,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadLLMProxy reads the stored URL and headers. Nothing stored means sessions use the provider.
func LoadLLMProxy(db *sql.DB) (llmproxy.Proxy, error) {
	proxyURL, err := LoadLLMProxyURL(db)
	if err != nil {
		return llmproxy.Proxy{}, err
	}
	headers, err := loadLLMProxyHeaders(db)
	if err != nil {
		return llmproxy.Proxy{}, err
	}
	return llmproxy.Proxy{
		URL:     proxyURL,
		Headers: headers,
	}, nil
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

func loadLLMProxyHeaders(db *sql.DB) (http.Header, error) {
	var body string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = 'llm_proxy_headers'`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return http.Header{}, nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(body), &values); err != nil {
		return nil, fmt.Errorf("session: stored llm proxy headers: %w", err)
	}
	headers := make(http.Header, len(values))
	for name, value := range values {
		headers.Set(name, value)
	}
	return headers, nil
}

// reservedLLMProxyHeaders are the ones the forwarder or Go's HTTP stack sets, or that describe the hop
// rather than the request. A value from the user would break the request or be thrown away.
var reservedLLMProxyHeaders = []string{
	"Accept-Encoding", "Connection", "Content-Encoding", "Content-Length", "Content-Type", "Forwarded",
	"Host", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "X-Api-Key",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip",
}

// NormalizeLLMProxyHeader trims and checks one header, and returns its name in canonical form.
//
// The value is never put in an error: it is usually a token.
func NormalizeLLMProxyHeader(rawName, rawValue string) (name, value string, err error) {
	name = strings.TrimSpace(rawName)
	value = strings.TrimSpace(rawValue)
	if !httpguts.ValidHeaderFieldName(name) {
		return "", "", fmt.Errorf("%q is not a header name", name)
	}
	name = http.CanonicalHeaderKey(name)
	if slices.Contains(reservedLLMProxyHeaders, name) ||
		strings.HasPrefix(name, "Proxy-") || strings.HasPrefix(name, "Sec-") {
		return "", "", fmt.Errorf("deepcove sets %s itself, so it can't be changed here", name)
	}
	if value == "" {
		return "", "", fmt.Errorf("%s needs a value", name)
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return "", "", fmt.Errorf("the value of %s has a character a header can't carry", name)
	}
	return name, value, nil
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
