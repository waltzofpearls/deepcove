package llmproxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveOverPipe runs a forwarder on one end of a pipe and returns the client end, which is what a guest
// connection looks like from the host's side.
func serveOverPipe(t *testing.T, upstream string, client *http.Client) net.Conn {
	t.Helper()
	return serveOverPipeFunc(t, func() string { return upstream }, client)
}

// serveOverPipeFunc is the same, for tests that change the address while the connection is open.
func serveOverPipeFunc(t *testing.T, upstream func() string, client *http.Client) net.Conn {
	t.Helper()
	forwarder := NewForwarder(upstream, nil, func() *http.Client { return client }, nil, nil)
	guest, host := net.Pipe()
	done := make(chan struct{})
	go func() {
		_ = forwarder.Serve(host)
		close(done)
	}()
	t.Cleanup(func() {
		_ = guest.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return after the guest hung up")
		}
	})
	if err := guest.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	return guest
}

func request(t *testing.T, conn net.Conn, br *bufio.Reader, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://gateway"+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

func TestForwarderCarriesRequestsToTheGateway(t *testing.T) {
	var gotPaths []string
	var gotForwardedFor []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		gotForwardedFor = append(gotForwardedFor, r.Header.Get("X-Forwarded-For"))
		_, _ = io.WriteString(w, "answer:"+r.URL.Path)
	}))
	defer upstream.Close()

	conn := serveOverPipe(t, upstream.URL, nil)
	br := bufio.NewReader(conn)

	// Two requests on one connection: an agent keeps the connection alive, so a forwarder that handled
	// only the first would look fine in a single-request test and stall in real use.
	for _, path := range []string{"/v1/messages", "/v1/models"} {
		resp := request(t, conn, br, path)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = resp.Body.Close()
		if got, want := string(body), "answer:"+path; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	}
	if len(gotPaths) != 2 {
		t.Fatalf("gateway saw %d requests, want 2", len(gotPaths))
	}
	// The gateway must learn nothing about the sandbox behind the hop.
	for i, v := range gotForwardedFor {
		if v != "" {
			t.Errorf("request %d carried X-Forwarded-For %q, want none", i, v)
		}
	}
}

func TestForwarderStreamsAnAnswer(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second\n")
	}))
	defer upstream.Close()

	conn := serveOverPipe(t, upstream.URL, nil)
	br := bufio.NewReader(conn)
	resp := request(t, conn, br, "/stream")
	defer func() { _ = resp.Body.Close() }()

	// The first chunk has to arrive before the gateway is done, or the answer was buffered.
	body := bufio.NewReader(resp.Body)
	line, err := body.ReadString('\n')
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if line != "first\n" {
		t.Errorf("first chunk = %q, want %q", line, "first\n")
	}
	close(release)
	rest, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if string(rest) != "second\n" {
		t.Errorf("rest = %q, want %q", string(rest), "second\n")
	}
}

// An address the node cannot use has to reach the agent as an error it can print, not as a dead
// connection it waits on.
func TestForwarderAnswersWhenTheAddressIsUnusable(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "none configured", url: ""},
		{name: "no scheme", url: "100.75.35.244:9000"},
		{name: "no host", url: "http://"},
		{name: "not a url", url: "://nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := serveOverPipe(t, tc.url, nil)
			resp := request(t, conn, bufio.NewReader(conn), "/v1/messages")
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "deepcove could not forward") {
				t.Errorf("body = %q, want it to say what went wrong", body)
			}
		})
	}
}

// The address is read per request, so changing it in the UI reaches a machine that is already running.
func TestForwarderFollowsTheAddressWhileRunning(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "second")
	}))
	defer second.Close()

	target := first.URL
	conn := serveOverPipeFunc(t, func() string { return target }, nil)
	br := bufio.NewReader(conn)

	for _, want := range []string{"first", "second"} {
		resp := request(t, conn, br, "/v1/messages")
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = resp.Body.Close()
		if string(body) != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
		target = second.URL
	}
}

func TestLLMEndpointNamesTheGuestPort(t *testing.T) {
	if got := LLMEndpoint(); !strings.HasSuffix(got, "9100") || !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("LLMEndpoint() = %q", got)
	}
}

// A proxy that is configured but unreachable has to say so to both sides: the agent needs a message it
// can print, and the host needs a record that it happened.
func TestForwarderReportsAnUnreachableProxy(t *testing.T) {
	// A server that is closed straight away gives us an address nothing is listening on.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var logged []error
	forwarder := NewForwarder(
		func() string { return deadURL },
		nil,
		func() *http.Client { return nil },
		func(err error) { logged = append(logged, err) },
		nil,
	)
	guest, host := net.Pipe()
	go func() { _ = forwarder.Serve(host) }()
	defer func() { _ = guest.Close() }()
	if err := guest.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	resp := request(t, guest, bufio.NewReader(guest), "/v1/messages")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// The address belongs in the message: "connection refused" alone does not say to what.
	if !strings.Contains(string(body), deadURL) {
		t.Errorf("body = %q, want it to name %s", body, deadURL)
	}
	if len(logged) == 0 {
		t.Error("the host was told nothing")
	}
}

// A machine several sessions share has to be accountable per session, and the id must not travel on to
// the proxy.
func TestForwarderAttributesRequestsToASession(t *testing.T) {
	var gotAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
	}))
	defer upstream.Close()

	var seen []string
	forwarder := NewForwarder(
		func() string { return upstream.URL },
		nil,
		func() *http.Client { return nil },
		nil,
		func(sessionID, method, path string) { seen = append(seen, sessionID+" "+method+" "+path) },
	)
	guest, host := net.Pipe()
	go func() { _ = forwarder.Serve(host) }()
	defer func() { _ = guest.Close() }()
	if err := guest.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+AuthToken("session-42"))
	if err := req.Write(guest); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(guest), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = resp.Body.Close()

	if len(seen) != 1 || seen[0] != "session-42 POST /v1/messages" {
		t.Errorf("recorded %v, want the session, method and path", seen)
	}
	if len(gotAuth) != 1 || strings.Contains(gotAuth[0], "session-42") {
		t.Errorf("the proxy saw %q; the session id must not leave this machine", gotAuth)
	}
}

func TestForwarder_route(t *testing.T) {
	type seen struct{ server, auth, apiKey string }
	var got []seen
	serverNamed := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = append(got, seen{name, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")})
		}))
	}
	proxy, provider := serverNamed("proxy"), serverNamed("provider")
	defer proxy.Close()
	defer provider.Close()
	keys := map[string]string{"with-key": "sk-ant-1234"}

	cases := []struct {
		name       string
		sessionID  string
		sentAPIKey string
		wantStatus int
		want       seen
	}{
		{name: "a session with the API key goes to the provider with it", sessionID: "with-key", wantStatus: http.StatusOK, want: seen{"provider", "", "sk-ant-1234"}},
		{name: "any other session goes to the proxy", sessionID: "without", wantStatus: http.StatusOK, want: seen{"proxy", "Bearer deepcove", ""}},
		{name: "a key the agent sends never reaches the proxy", sessionID: "without", sentAPIKey: "sk-from-the-guest", wantStatus: http.StatusOK, want: seen{"proxy", "Bearer deepcove", ""}},
		{name: "a session whose key cannot be read is told why", sessionID: "broken", wantStatus: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			forwarder := NewForwarder(
				func() string { return proxy.URL },
				func(sessionID string) (string, error) {
					if sessionID == "broken" {
						return "", errors.New("no API key is saved")
					}
					return keys[sessionID], nil
				},
				func() *http.Client { return nil },
				nil, nil,
			)
			forwarder.providerURL = provider.URL
			guest, host := net.Pipe()
			go func() { _ = forwarder.Serve(host) }()
			defer func() { _ = guest.Close() }()
			_ = guest.SetDeadline(time.Now().Add(5 * time.Second))

			req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9100/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer "+AuthToken(tc.sessionID))
			if tc.sentAPIKey != "" {
				req.Header.Set("X-Api-Key", tc.sentAPIKey)
			}
			if err := req.Write(guest); err != nil {
				t.Fatalf("write: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(guest), req)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && (len(got) != 1 || got[0] != tc.want) {
				t.Errorf("upstream saw %+v, want %+v", got, tc.want)
			}
		})
	}
}
