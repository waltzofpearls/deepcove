package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestNormalizeLLMProxyURL(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "no proxy configured", input: "", want: ""},
		{name: "blank is the same as none", input: "   ", want: ""},
		{
			name:  "a host and port, which is the usual case",
			input: "http://100.75.35.244:9000",
			want:  "http://100.75.35.244:9000",
		},
		{name: "a MagicDNS name", input: "http://ai.example.ts.net", want: "http://ai.example.ts.net"},
		{name: "https is allowed too", input: "https://ai.example.ts.net", want: "https://ai.example.ts.net"},
		{name: "surrounding space is trimmed", input: "  http://ai:9000  ", want: "http://ai:9000"},
		{name: "a bare trailing slash is not a path", input: "http://ai:9000/", want: "http://ai:9000"},

		// Agents append their own paths to this, so a path here would break in a confusing place.
		{name: "a path is rejected", input: "http://ai:9000/v1", wantErr: true},
		{name: "a query is rejected", input: "http://ai:9000?x=1", wantErr: true},
		{name: "credentials are rejected", input: "http://user:pass@ai:9000", wantErr: true},
		{name: "a missing scheme is rejected", input: "100.75.35.244:9000", wantErr: true},
		{name: "a non http scheme is rejected", input: "ftp://ai:9000", wantErr: true},
		{name: "a missing host is rejected", input: "http://", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeLLMProxyURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeLLMProxyURL(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The machine is shared, so nothing about the agent's credentials belongs in its environment. That
// choice is per session and lives in the launch command instead.
func TestMachineEnvironmentCarriesNoAgentCredentials(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")

	sess := &Session{ID: "s1", Name: "n", Agent: "claude", Image: "img", LoginMode: LoginWithLLMProxy}
	agent, err := resolveAgent("claude")
	if err != nil {
		t.Fatalf("resolveAgent: %v", err)
	}
	spec := svc.buildSandboxSpec(sess, agent, nil, fake)

	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := spec.Env[k]; ok {
			t.Errorf("%s = %q reached the machine environment", k, v)
		}
	}
	// The old fix for the sign-in prompt was a file every session on the machine shared, which is what
	// made opting out impossible.
	if entrypoint := strings.Join(spec.Cmd, " "); strings.Contains(entrypoint, "managed-settings.json") {
		t.Error("the entrypoint still writes managed settings")
	}
}

func TestAgentLaunchCommand(t *testing.T) {
	forwarding := forwardingSandbox{newFakeSandbox()}
	codexClears := []string{
		"env", "-u", "OPENAI_API_KEY", "-u", "OPENAI_BASE_URL", "-u", "CODEX_API_KEY", "-u", "CODEX_ACCESS_TOKEN",
		"-u", "DEEPCOVE_LLM_BASE_URL", "-u", "DEEPCOVE_LLM_TOKEN", "DEEPCOVE_SESSION_ID=s1",
	}
	cases := []struct {
		name     string
		agent    string
		proxyURL string
		runtime  sandbox.Sandbox
		mode     LoginMode
		want     []string
	}{
		{
			name:     "Codex in a VM reaches the proxy through the local hop",
			agent:    "codex",
			proxyURL: "http://100.75.35.244:9000",
			runtime:  forwarding,
			mode:     LoginWithLLMProxy,
			want: append(slices.Clone(codexClears),
				"DEEPCOVE_LLM_BASE_URL=http://127.0.0.1:9100", "DEEPCOVE_LLM_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath("codex")),
		},
		{
			name:     "Codex without forwarding calls the proxy itself",
			agent:    "codex",
			proxyURL: "http://100.75.35.244:9000",
			mode:     LoginWithLLMProxy,
			want: append(slices.Clone(codexClears),
				"DEEPCOVE_LLM_BASE_URL=http://100.75.35.244:9000", "DEEPCOVE_LLM_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath("codex")),
		},
		{
			name:    "a session that wants nothing is cleared of any address the machine carries",
			runtime: forwarding,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
		{
			name:     "a forwarding runtime points the agent at the local hop",
			proxyURL: "http://100.75.35.244:9000",
			runtime:  forwarding,
			mode:     LoginWithLLMProxy,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1",
				"ANTHROPIC_BASE_URL=http://127.0.0.1:9100", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
		{
			name:     "without forwarding the agent calls the proxy itself",
			proxyURL: "http://100.75.35.244:9000",
			mode:     LoginWithLLMProxy,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1",
				"ANTHROPIC_BASE_URL=http://100.75.35.244:9000", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
		{
			name:    "asking for a proxy the node does not have leaves the agent on its own login",
			runtime: forwarding,
			mode:    LoginWithLLMProxy,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
		{
			name:    "the API key goes through the local hop, which adds the key on the host",
			runtime: forwarding,
			mode:    LoginWithAPIKey,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1",
				"ANTHROPIC_BASE_URL=http://127.0.0.1:9100", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
		{
			name: "no hop to add the API key leaves the agent on its own login",
			mode: LoginWithAPIKey,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath("claude"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			svc.SetLLMProxyURL(tc.proxyURL)
			runtime := tc.runtime
			if runtime == nil {
				runtime = fake
			}
			agent := tc.agent
			if agent == "" {
				agent = "claude"
			}
			sess := &Session{ID: "s1", Agent: agent, LoginMode: tc.mode}
			got, err := svc.agentLaunchCommand(sess, runtime)
			if err != nil {
				t.Fatalf("agentLaunchCommand: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLLMProxyEndpoints(t *testing.T) {
	svc, _, _, database := newTestService(t)
	svc.SetLLMProxyStore(NewDuckDBLLMProxyStore(database))
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)

	get := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/llm-proxy", nil))
		var body struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
		return body.URL
	}
	put := func(url string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/llm-proxy", strings.NewReader(`{"url":"`+url+`"}`))
		mux.ServeHTTP(w, r)
		return w
	}

	if got := get(); got != "" {
		t.Errorf("before setting one: %q, want empty", got)
	}

	// The trailing slash is dropped by normalizing, which is the point of doing it server side.
	if w := put("http://100.75.35.244:9000/"); w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", w.Code, w.Body)
	}
	if got := get(); got != "http://100.75.35.244:9000" {
		t.Errorf("got %q, want the normalized url", got)
	}

	// A url an agent would append a path to is refused rather than quietly trimmed.
	if w := put("http://host:9000/v1/messages"); w.Code != http.StatusBadRequest {
		t.Errorf("a url with a path: status %d, want 400", w.Code)
	}
	if got := get(); got != "http://100.75.35.244:9000" {
		t.Errorf("a refused url changed the setting to %q", got)
	}

	// It survives a restart: a fresh service reading the same database sees it.
	stored, err := LoadLLMProxyURL(database)
	if err != nil {
		t.Fatalf("LoadLLMProxyURL: %v", err)
	}
	if stored != "http://100.75.35.244:9000" {
		t.Errorf("stored %q, want the url the API saved", stored)
	}

	if w := put(""); w.Code != http.StatusOK {
		t.Fatalf("clearing: status %d, body %s", w.Code, w.Body)
	}
	if got := get(); got != "" {
		t.Errorf("after clearing: %q, want empty", got)
	}
}

// forwardingSandbox is a runtime that carries LLM traffic itself, like the VM runtime.
type forwardingSandbox struct {
	*fakeSandbox
}

func (forwardingSandbox) LLMEndpoint() string { return "http://127.0.0.1:9100" }

func TestLLMProxyEndpoint(t *testing.T) {
	plain := newFakeSandbox()
	forwarding := forwardingSandbox{newFakeSandbox()}
	cases := []struct {
		name     string
		runtime  sandbox.Sandbox
		proxyURL string
		want     string
	}{
		{
			name:    "no proxy configured",
			runtime: forwarding,
		},
		{
			name:     "a runtime that cannot forward sends the agent straight to the proxy",
			runtime:  plain,
			proxyURL: "http://100.75.35.244:9000",
			want:     "http://100.75.35.244:9000",
		},
		{
			name:     "a forwarding runtime keeps the traffic off the sandbox network",
			runtime:  forwarding,
			proxyURL: "http://100.75.35.244:9000",
			want:     "http://127.0.0.1:9100",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llmProxyEndpoint(tc.runtime, tc.proxyURL); got != tc.want {
				t.Errorf("endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// A change from the UI must not race a session being built. This caught a real race before the lock.
func TestLLMProxyURLIsSafeUnderConcurrentUse(t *testing.T) {
	svc, _, _, database := newTestService(t)
	svc.SetLLMProxyStore(NewDuckDBLLMProxyStore(database))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			if err := svc.SaveLLMProxy("http://100.75.35.244:9000", nil); err != nil {
				t.Errorf("save: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			if _, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"}); err != nil {
				t.Errorf("create: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestEditSharedFoldersRefusesAWorkspaceSession(t *testing.T) {
	svc, lister := workspaceService(t)
	sess := onWorkspace(t, svc, "shared-one", "")
	before := lister.liveCount()

	_, err := svc.EditSharedFolders(context.Background(), sess.ID,
		[]SharedFolderRequest{{HostPath: t.TempDir(), GuestPath: "/workspace"}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	// The machine other sessions are using must still be there.
	if after := lister.liveCount(); after != before {
		t.Errorf("live sandboxes went from %d to %d", before, after)
	}
}

var errTmuxBroken = errors.New("tmux is not there")

func TestNormalizeLLMProxyHeader(t *testing.T) {
	cases := []struct {
		name      string
		inName    string
		inValue   string
		wantName  string
		wantValue string
		wantErr   bool
	}{
		{
			name:      "a token header",
			inName:    "Border0-Token",
			inValue:   "b0-secret",
			wantName:  "Border0-Token",
			wantValue: "b0-secret",
		},
		{
			name:      "the name is made canonical",
			inName:    "border0-token",
			inValue:   "b0-secret",
			wantName:  "Border0-Token",
			wantValue: "b0-secret",
		},
		{
			name:      "surrounding space is trimmed",
			inName:    " X-Team ",
			inValue:   "  infra  ",
			wantName:  "X-Team",
			wantValue: "infra",
		},
		{
			name:      "Authorization may be set",
			inName:    "authorization",
			inValue:   "Bearer gw-1234",
			wantName:  "Authorization",
			wantValue: "Bearer gw-1234",
		},
		{
			name:    "an empty name",
			inName:  "",
			inValue: "x",
			wantErr: true,
		},
		{
			name:    "a name with a space",
			inName:  "Border0 Token",
			inValue: "x",
			wantErr: true,
		},
		{
			name:    "a name with a colon",
			inName:  "Border0-Token:",
			inValue: "x",
			wantErr: true,
		},
		{
			name:    "an empty value",
			inName:  "X-Team",
			inValue: "  ",
			wantErr: true,
		},
		// A newline would start a header of its own on the wire.
		{
			name:    "a value with a newline",
			inName:  "X-Team",
			inValue: "infra\r\nHost: evil",
			wantErr: true,
		},
		{
			name:    "a value with a NUL",
			inName:  "X-Team",
			inValue: "in\x00fra",
			wantErr: true,
		},
		{
			name:    "Host is deepcove's",
			inName:  "host",
			inValue: "ai.corp.ts.net",
			wantErr: true,
		},
		{
			name:    "Content-Length is deepcove's",
			inName:  "Content-Length",
			inValue: "1",
			wantErr: true,
		},
		{
			name:    "a hop by hop header",
			inName:  "Connection",
			inValue: "close",
			wantErr: true,
		},
		{
			name:    "the provider key header",
			inName:  "x-api-key",
			inValue: "sk-ant-1234",
			wantErr: true,
		},
		{
			name:    "a forwarding header",
			inName:  "X-Forwarded-For",
			inValue: "10.0.0.1",
			wantErr: true,
		},
		{
			name:    "a Proxy- header",
			inName:  "Proxy-Authorization",
			inValue: "Basic x",
			wantErr: true,
		},
		{
			name:    "a Sec- header",
			inName:  "Sec-Fetch-Site",
			inValue: "none",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotValue, err := NormalizeLLMProxyHeader(tc.inName, tc.inValue)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if err != nil {
				if value := strings.TrimSpace(tc.inValue); value != "" && strings.Contains(err.Error(), value) {
					t.Errorf("error %q repeats the value", err)
				}
				return
			}
			if gotName != tc.wantName || gotValue != tc.wantValue {
				t.Errorf("got %q: %q, want %q: %q", gotName, gotValue, tc.wantName, tc.wantValue)
			}
		})
	}
}

func TestLLMProxyHeaderEndpoints(t *testing.T) {
	svc, _, _, database := newTestService(t)
	svc.SetLLMProxyStore(NewDuckDBLLMProxyStore(database))
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/llm-proxy", strings.NewReader(body)))
		return w
	}
	names := func(w *httptest.ResponseRecorder) []string {
		t.Helper()
		var body struct {
			Headers []string `json:"headers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
		return body.Headers
	}
	const border0 = "https://rollie-llm-sandbox-rollie-vpn.border0.io"

	w := put(`{"url":"` + border0 + `","headers":[{"name":"border0-token","value":"b0-secret"},{"name":"X-Team","value":"infra"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", w.Code, w.Body)
	}
	if got := names(w); !slices.Equal(got, []string{"Border0-Token", "X-Team"}) {
		t.Errorf("names = %v, want both, canonical", got)
	}
	// Values are write only.
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/llm-proxy", nil))
	if strings.Contains(get.Body.String(), "b0-secret") {
		t.Errorf("GET returned a value: %s", get.Body)
	}

	// A refused header is named, so the UI can show the reason on its row, and nothing is saved.
	w = put(`{"url":"` + border0 + `","headers":[{"name":"Host","value":"ai.corp.ts.net"}]}`)
	var refused struct {
		Header string `json:"header"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refused)
	if w.Code != http.StatusBadRequest || refused.Header != "Host" {
		t.Errorf("a reserved name: status %d, body %s, want 400 naming Host", w.Code, w.Body)
	}

	// A header sent with no value keeps the saved one. One left out is removed.
	if w := put(`{"url":"` + border0 + `","headers":[{"name":"Border0-Token"}]}`); w.Code != http.StatusOK {
		t.Fatalf("keep: status %d, body %s", w.Code, w.Body)
	}
	stored, err := LoadLLMProxy(database)
	if err != nil {
		t.Fatalf("LoadLLMProxy: %v", err)
	}
	if stored.URL != border0 || stored.Headers.Get("Border0-Token") != "b0-secret" || len(stored.Headers) != 1 {
		t.Errorf("stored %v, want the URL and only the kept token", stored)
	}
	if got := svc.LLMProxy().Headers.Get("Border0-Token"); got != "b0-secret" {
		t.Errorf("the forwarder would send %q, want the saved value", got)
	}

	// A UI older than headers sends the URL alone. That keeps them, and an empty list removes them.
	if w := put(`{"url":"` + border0 + `"}`); w.Code != http.StatusOK || len(names(w)) != 1 {
		t.Errorf("an older UI's save: status %d, body %s, want the token kept", w.Code, w.Body)
	}
	if w := put(`{"url":"` + border0 + `","headers":[]}`); w.Code != http.StatusOK || len(names(w)) != 0 {
		t.Errorf("an empty list: status %d, body %s, want no headers left", w.Code, w.Body)
	}
}

func TestService_handleTestLLMProxy(t *testing.T) {
	var gotToken string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Border0-Token")
	}))
	defer proxy.Close()
	svc, _, _, _ := newTestService(t)
	svc.SetLLMProxyURL(proxy.URL)
	svc.SetLLMProxyHeaders(http.Header{"Border0-Token": {"b0-secret"}})
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)

	// A UI older than testing the form sends no body.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/llm-proxy/test", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), proxy.URL) {
		t.Fatalf("no body: status %d, body %s, want the saved proxy tested", w.Code, w.Body)
	}
	if gotToken != "b0-secret" {
		t.Errorf("the proxy saw token %q, want the saved one", gotToken)
	}
}

func TestApplyLLMProxyEdits(t *testing.T) {
	const border0 = "https://rollie-llm-sandbox-rollie-vpn.border0.io"
	saved := llmproxy.Proxy{
		URL:     border0,
		Headers: http.Header{"Border0-Token": {"b0-secret"}},
	}
	value := func(text string) *string {
		return &text
	}
	cases := []struct {
		name          string
		saved         llmproxy.Proxy
		url           string
		edits         []LLMProxyHeaderEdit
		wantHeaders   http.Header
		wantErr       bool
		wantErrHeader string
	}{
		{
			name: "the first save sends the URL and the headers together",
			url:  border0,
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "border0-token",
					Value: value("b0-secret"),
				},
			},
			wantHeaders: http.Header{
				"Border0-Token": {"b0-secret"},
			},
		},
		{
			name:  "a header with no value keeps the saved one",
			saved: saved,
			url:   border0,
			edits: []LLMProxyHeaderEdit{{Name: "Border0-Token"}},
			wantHeaders: http.Header{
				"Border0-Token": {"b0-secret"},
			},
		},
		{
			name:  "a header with a value replaces the saved one",
			saved: saved,
			url:   border0,
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "Border0-Token",
					Value: value("b0-new"),
				},
			},
			wantHeaders: http.Header{
				"Border0-Token": {"b0-new"},
			},
		},
		{
			name:        "a header left out is removed",
			saved:       saved,
			url:         border0,
			edits:       []LLMProxyHeaderEdit{},
			wantHeaders: http.Header{},
		},
		{
			name:  "a UI older than headers sends no list, and the same host keeps them",
			saved: saved,
			url:   border0,
			wantHeaders: http.Header{
				"Border0-Token": {"b0-secret"},
			},
		},
		{
			name:        "a UI older than headers sends no list, and a new host drops them",
			saved:       saved,
			url:         "https://gateway.example.com",
			wantHeaders: http.Header{},
		},
		{
			name:  "a new port or scheme on the same host keeps the saved value",
			saved: saved,
			url:   "http://rollie-llm-sandbox-rollie-vpn.border0.io:8443",
			edits: []LLMProxyHeaderEdit{{Name: "Border0-Token"}},
			wantHeaders: http.Header{
				"Border0-Token": {"b0-secret"},
			},
		},
		{
			name:  "the host name is compared without case",
			saved: saved,
			url:   "https://Rollie-LLM-Sandbox-Rollie-VPN.border0.io",
			edits: []LLMProxyHeaderEdit{{Name: "Border0-Token"}},
			wantHeaders: http.Header{
				"Border0-Token": {"b0-secret"},
			},
		},
		{
			name:          "a new host refuses a saved value",
			saved:         saved,
			url:           "https://gateway.example.com",
			edits:         []LLMProxyHeaderEdit{{Name: "Border0-Token"}},
			wantErr:       true,
			wantErrHeader: "Border0-Token",
		},
		{
			name:  "a new host takes a value entered again",
			saved: saved,
			url:   "https://gateway.example.com",
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "Border0-Token",
					Value: value("gw-1234"),
				},
			},
			wantHeaders: http.Header{
				"Border0-Token": {"gw-1234"},
			},
		},
		{
			name:          "clearing the URL refuses a saved value",
			saved:         saved,
			url:           "",
			edits:         []LLMProxyHeaderEdit{{Name: "Border0-Token"}},
			wantErr:       true,
			wantErrHeader: "Border0-Token",
		},
		{
			name: "headers with no URL are refused",
			url:  "",
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "Border0-Token",
					Value: value("b0-secret"),
				},
			},
			wantErr: true,
		},
		{
			name:          "a header with no saved value needs one",
			url:           border0,
			edits:         []LLMProxyHeaderEdit{{Name: "X-Team"}},
			wantErr:       true,
			wantErrHeader: "X-Team",
		},
		{
			name: "a refused header is named",
			url:  border0,
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "Host",
					Value: value("ai.corp.ts.net"),
				},
			},
			wantErr:       true,
			wantErrHeader: "Host",
		},
		{
			name: "the same header twice is refused",
			url:  border0,
			edits: []LLMProxyHeaderEdit{
				{
					Name:  "X-Team",
					Value: value("infra"),
				},
				{
					Name:  "x-team",
					Value: value("web"),
				},
			},
			wantErr:       true,
			wantErrHeader: "X-Team",
		},
		{
			name:    "a URL that is not usable is refused",
			url:     "ftp://rollie-llm-sandbox-rollie-vpn.border0.io",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyLLMProxyEdits(tc.saved, tc.url, tc.edits)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			var headerErr LLMProxyHeaderError
			errors.As(err, &headerErr)
			if headerErr.Name != tc.wantErrHeader {
				t.Errorf("refused header = %q, want %q", headerErr.Name, tc.wantErrHeader)
			}
			if err == nil && !reflect.DeepEqual(got.Headers, tc.wantHeaders) {
				t.Errorf("headers = %v, want %v", got.Headers, tc.wantHeaders)
			}
		})
	}
}

func TestLLMProxyWithURL(t *testing.T) {
	saved := llmproxy.Proxy{
		URL:     "https://rollie-llm-sandbox-rollie-vpn.border0.io",
		Headers: http.Header{"Border0-Token": {"b0-secret"}},
	}
	cases := []struct {
		name        string
		url         string
		wantHeaders int
	}{
		{
			name:        "the same host keeps the headers",
			url:         "https://rollie-llm-sandbox-rollie-vpn.border0.io:8443",
			wantHeaders: 1,
		},
		{
			name: "a new host drops them",
			url:  "https://gateway.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LLMProxyWithURL(saved, tc.url)
			if err != nil {
				t.Fatalf("LLMProxyWithURL: %v", err)
			}
			if len(got.Headers) != tc.wantHeaders {
				t.Errorf("headers = %v, want %d", got.Headers, tc.wantHeaders)
			}
		})
	}
}

func TestService_TestLLMProxy(t *testing.T) {
	var redirectedTo bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedTo = true
	}))
	defer elsewhere.Close()
	var gotToken string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Border0-Token")
		http.Redirect(w, r, elsewhere.URL+"/login?state=b0-state", http.StatusFound)
	}))
	defer proxy.Close()

	svc, _, _, _ := newTestService(t)
	svc.SetLLMProxyURL(proxy.URL)
	svc.SetLLMProxyHeaders(http.Header{"Border0-Token": {"b0-secret"}})

	// No value in the form, so the test sends the saved one, the same as a session would.
	result, err := svc.TestLLMProxy(context.Background(), proxy.URL, []LLMProxyHeaderEdit{{Name: "Border0-Token"}})
	if err != nil {
		t.Fatalf("TestLLMProxy: %v", err)
	}
	if gotToken != "b0-secret" {
		t.Errorf("the proxy saw token %q, want the saved one", gotToken)
	}
	// Following it would hand the token to another host.
	if redirectedTo {
		t.Error("the check followed a redirect")
	}
	if !strings.Contains(result.Result, "302") || !strings.Contains(result.Result, elsewhere.URL+"/login") {
		t.Errorf("result = %q, want the proxy's own answer and where it points", result.Result)
	}
	// A login redirect can carry a token in its query.
	if strings.Contains(result.Result, "b0-state") {
		t.Errorf("result = %q repeats the redirect's query", result.Result)
	}

	proxy.Close()
	result, err = svc.TestLLMProxy(context.Background(), proxy.URL, []LLMProxyHeaderEdit{{Name: "Border0-Token"}})
	if err != nil || result.Verdict != LLMProxyUnreachable {
		t.Errorf("a closed proxy: %+v, %v, want unreachable and no error", result, err)
	}
}

func TestLLMProxyTestVerdict(t *testing.T) {
	const border0 = "https://rollie-llm-sandbox-rollie-vpn.border0.io"
	cases := []struct {
		name     string
		status   int
		location string
		want     LLMProxyTestVerdict
	}{
		{
			name:   "200",
			status: http.StatusOK,
			want:   LLMProxyReached,
		},
		{
			name:   "a proxy that refuses a bare GET",
			status: http.StatusMethodNotAllowed,
			want:   LLMProxyReached,
		},
		{
			name:     "a redirect to the proxy's own page",
			status:   http.StatusFound,
			location: border0 + "/chat",
			want:     LLMProxyReached,
		},
		{
			name:     "a relative redirect stays on the proxy",
			status:   http.StatusFound,
			location: "/chat",
			want:     LLMProxyReached,
		},
		{
			name:   "401",
			status: http.StatusUnauthorized,
			want:   LLMProxyRejected,
		},
		{
			name:   "403",
			status: http.StatusForbidden,
			want:   LLMProxyRejected,
		},
		{
			name:   "407",
			status: http.StatusProxyAuthRequired,
			want:   LLMProxyRejected,
		},
		{
			name:     "a redirect to a login page on another host",
			status:   http.StatusFound,
			location: "https://login.border0.io/authorize",
			want:     LLMProxyRejected,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, border0, nil)
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     http.Header{},
				Request:    req,
			}
			if tc.location != "" {
				resp.Header.Set("Location", tc.location)
			}
			if got := llmProxyTestVerdict(border0, resp); got != tc.want {
				t.Errorf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}
