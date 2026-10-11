package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

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
	cases := []struct {
		name     string
		proxyURL string
		runtime  sandbox.Sandbox
		mode     LoginMode
		want     []string
	}{
		{
			name:    "a session that wants nothing is cleared of any address the machine carries",
			runtime: forwarding,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath,
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
				"ANTHROPIC_BASE_URL=http://127.0.0.1:9100", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath,
			},
		},
		{
			name:     "without forwarding the agent calls the proxy itself",
			proxyURL: "http://100.75.35.244:9000",
			mode:     LoginWithLLMProxy,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1",
				"ANTHROPIC_BASE_URL=http://100.75.35.244:9000", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath,
			},
		},
		{
			name:    "asking for a proxy the node does not have leaves the agent on its own login",
			runtime: forwarding,
			mode:    LoginWithLLMProxy,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath,
			},
		},
		{
			name:    "the API key goes through the local hop, which adds the key on the host",
			runtime: forwarding,
			mode:    LoginWithAPIKey,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1",
				"ANTHROPIC_BASE_URL=http://127.0.0.1:9100", "ANTHROPIC_AUTH_TOKEN=deepcove-s1", sandbox.SandboxedAgentLaunchPath,
			},
		},
		{
			name: "no hop to add the API key leaves the agent on its own login",
			mode: LoginWithAPIKey,
			want: []string{
				"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL",
				"DEEPCOVE_SESSION_ID=s1", sandbox.SandboxedAgentLaunchPath,
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
			sess := &Session{ID: "s1", LoginMode: tc.mode}
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
			if _, err := svc.SaveLLMProxy("http://100.75.35.244:9000"); err != nil {
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

func TestEditMountsRefusesAWorkspaceSession(t *testing.T) {
	svc, lister := workspaceService(t)
	sess := onWorkspace(t, svc, "shared-one", "")
	before := lister.liveCount()

	_, err := svc.EditMounts(context.Background(), sess.ID,
		[]MountRequest{{HostPath: t.TempDir(), ContainerPath: "/workspace"}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	// The machine other sessions are using must still be there.
	if after := lister.liveCount(); after != before {
		t.Errorf("live sandboxes went from %d to %d", before, after)
	}
}

var errTmuxBroken = errors.New("tmux is not there")
