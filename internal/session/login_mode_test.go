package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

func TestService_loginModeOrDefault(t *testing.T) {
	agent, err := resolveAgent("claude")
	if err != nil {
		t.Fatal(err)
	}
	vm := forwardingSandbox{newFakeSandbox()}
	noSandbox := newFakeSandbox()
	cases := []struct {
		name          string
		defaultMode   LoginMode
		apiKey        string
		proxyURL      string
		proxyHeaders  http.Header
		asked         LoginMode
		runtime       sandbox.Sandbox
		onWorkspaceVM bool
		want          LoginMode
		wantErr       bool
	}{
		{name: "nothing asked takes the default", defaultMode: LoginShared, runtime: vm, want: LoginShared},
		{name: "asked beats the default", defaultMode: LoginShared, asked: LoginInSession, runtime: vm, want: LoginInSession},
		{name: "a default this session cannot use falls back to its own login", defaultMode: LoginShared, runtime: noSandbox, want: LoginInSession},
		{name: "a workspace VM session may share the machine's login", defaultMode: LoginShared, runtime: noSandbox, onWorkspaceVM: true, want: LoginShared},
		{name: "asking to share with no sandbox is refused", asked: LoginShared, runtime: noSandbox, wantErr: true},
		{name: "the API key with one saved", asked: LoginWithAPIKey, apiKey: "sk-ant-1234", runtime: vm, want: LoginWithAPIKey},
		{name: "the API key with none saved is refused", asked: LoginWithAPIKey, runtime: vm, wantErr: true},
		{name: "the API key default with none saved falls back", defaultMode: LoginWithAPIKey, runtime: vm, want: LoginInSession},
		{name: "the LLM proxy with none set is refused", asked: LoginWithLLMProxy, runtime: vm, wantErr: true},
		{name: "the LLM proxy when set", asked: LoginWithLLMProxy, proxyURL: "http://100.75.35.244:9000", runtime: vm, want: LoginWithLLMProxy},
		{
			name:         "the LLM proxy with headers on a VM",
			asked:        LoginWithLLMProxy,
			proxyURL:     "http://100.75.35.244:9000",
			proxyHeaders: http.Header{"Border0-Token": {"b0"}},
			runtime:      vm,
			want:         LoginWithLLMProxy,
		},
		{
			name:     "the LLM proxy with no sandbox",
			asked:    LoginWithLLMProxy,
			proxyURL: "http://100.75.35.244:9000",
			runtime:  noSandbox,
			want:     LoginWithLLMProxy,
		},
		{
			name:         "the LLM proxy with headers and no sandbox is refused",
			asked:        LoginWithLLMProxy,
			proxyURL:     "http://100.75.35.244:9000",
			proxyHeaders: http.Header{"Border0-Token": {"b0"}},
			runtime:      noSandbox,
			wantErr:      true,
		},
		{name: "a mode the agent does not know is refused", asked: "magic", runtime: vm, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, database := newTestService(t)
			store := NewSharedLoginStore(database)
			if tc.defaultMode != "" {
				_ = store.SetDefaultLoginMode("claude", tc.defaultMode)
			}
			_ = store.SetAPIKey("claude", tc.apiKey)
			svc.sharedLoginStore = store
			svc.SetLLMProxyURL(tc.proxyURL)
			svc.SetLLMProxyHeaders(tc.proxyHeaders)

			got, err := svc.loginModeOrDefault(tc.asked, agent, tc.runtime, tc.onWorkspaceVM)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChangesAgentEnv(t *testing.T) {
	cases := []struct {
		from, to LoginMode
		want     bool
	}{
		{LoginShared, LoginInSession, false},
		{LoginInSession, LoginShared, false},
		{LoginShared, LoginWithAPIKey, true},
		{LoginWithLLMProxy, LoginShared, true},
		{LoginWithAPIKey, LoginWithLLMProxy, true},
		{LoginWithAPIKey, LoginWithAPIKey, false},
	}
	for _, tc := range cases {
		if got := changesAgentEnv(tc.from, tc.to); got != tc.want {
			t.Errorf("changesAgentEnv(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestService_SetLoginMode(t *testing.T) {
	ctx := context.Background()
	svc, fake, _, _ := newTestService(t)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "keep-me"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)

	// What tmux answers when the agent restarts: stopped, then confirmed gone, then started again.
	restart := []execResponse{{exit: 0}, {exit: 1}, {exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}}
	cases := []struct {
		body       string
		execs      []execResponse
		wantStatus int
		wantMode   LoginMode
	}{
		{body: `{"login_mode": "llm_proxy"}`, execs: restart, wantStatus: http.StatusOK, wantMode: LoginWithLLMProxy},
		{body: `{"name": "renamed"}`, wantStatus: http.StatusOK, wantMode: LoginWithLLMProxy},
		{body: `{"login_mode": "in_session"}`, execs: restart, wantStatus: http.StatusOK, wantMode: LoginInSession},
		{body: `{"login_mode": "shared"}`, wantStatus: http.StatusBadRequest, wantMode: LoginInSession},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			fake.execResponses = append([]execResponse(nil), tc.execs...)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sess.ID, strings.NewReader(tc.body)))
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d, body %s", w.Code, w.Body)
			}
			reloaded, err := svc.Get(ctx, sess.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if reloaded.LoginMode != tc.wantMode {
				t.Errorf("login mode = %q, want %q", reloaded.LoginMode, tc.wantMode)
			}
			if w.Code == http.StatusOK {
				var got Session
				_ = json.Unmarshal(w.Body.Bytes(), &got)
				if got.LoginMode != tc.wantMode || !strings.HasPrefix(got.Name, "keep-me") && got.Name != "renamed" {
					t.Errorf("reply = %q %q", got.Name, got.LoginMode)
				}
			}
		})
	}
}

func TestService_APIKeyFor(t *testing.T) {
	ctx := context.Background()
	svc, _, _, database := newTestService(t)
	store := NewSharedLoginStore(database)
	svc.sharedLoginStore = store
	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cases := []struct {
		name      string
		mode      LoginMode
		saved     string
		sandboxID sandbox.ID
		want      string
		wantErr   bool
	}{
		{name: "another mode gets no key", mode: LoginInSession, saved: "sk-ant-1234"},
		{name: "the API key mode gets the key", mode: LoginWithAPIKey, saved: "sk-ant-1234", want: "sk-ant-1234"},
		{name: "the API key mode with the key removed", mode: LoginWithAPIKey, wantErr: true},
		{name: "another VM naming this session gets no key", mode: LoginWithAPIKey, saved: "sk-ant-1234", sandboxID: "another-vm", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = store.SetAPIKey("claude", tc.saved)
			if err := svc.repo.UpdateLoginMode(ctx, placeholderOwner, sess.ID, tc.mode, sess.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			sandboxID := tc.sandboxID
			if sandboxID == "" {
				sandboxID = sandbox.ID(sess.SandboxID)
			}
			got, err := svc.APIKeyFor(sandboxID, sess.ID)
			if (err != nil) != tc.wantErr || got.Key != tc.want {
				t.Errorf("APIKeyFor = %q, %v; want %q, an error: %v", got.Key, err, tc.want, tc.wantErr)
			}
			if got.Key != "" && got.Provider != agents["claude"].API.Provider {
				t.Errorf("APIKeyFor sends the key to %+v, want Claude's provider", got.Provider)
			}
		})
	}
}

func TestSetLoginMode_PutsTheAgentBackWhenTheRestartFails(t *testing.T) {
	ctx := context.Background()
	svc, fake, _, _ := newTestService(t)
	svc.SetLLMProxyStore(NewDuckDBLLMProxyStore(nil))
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	sess := mustRunning(t, svc)

	// kill, then a start that fails at new-session, then the start that puts it back.
	fake.execResponses = []execResponse{
		{exit: 0},            // kill-session
		{exit: 1},            // has-session: the old agent is gone
		{exit: 1},            // has-session: nothing running
		{exit: 0},            // mkdir
		{err: errTmuxBroken}, // new-session fails
		{exit: 1},            // has-session again, for the restart
		{exit: 0},            // mkdir
		{exit: 0},            // new-session succeeds
		{exit: 0},            // set-option
	}
	fake.execCalls = nil

	if _, err := svc.SetLoginMode(ctx, sess.ID, LoginWithLLMProxy); err == nil {
		t.Fatal("want an error, got nil")
	}
	// The setting went back, so the row matches the agent that is actually running.
	after, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.LoginMode == LoginWithLLMProxy {
		t.Error("the session still says it uses the proxy, but its agent was started without it")
	}
	if len(fake.execCalls) != 9 {
		t.Errorf("made %d exec calls, want the failed start and the one that recovered: %v",
			len(fake.execCalls), fake.execCalls)
	}
}

// startAskingSandbox asks the sharing check from inside Start, as a booting guest does.
type startAskingSandbox struct {
	forwardingSandbox
	svc       *Service
	sessionID string
	shares    bool
	err       error
}

func (s *startAskingSandbox) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.ID, error) {
	s.sessionID = spec.SessionID
	return s.forwardingSandbox.Create(ctx, spec)
}

func (s *startAskingSandbox) Start(ctx context.Context, sandboxID sandbox.ID) error {
	s.shares, s.err = s.svc.sharesLogin("claude", s.sessionID)
	return s.forwardingSandbox.Start(ctx, sandboxID)
}

func TestService_sharesLogin_WhileTheMachineBoots(t *testing.T) {
	cases := []struct {
		mode LoginMode
		want bool
	}{
		{mode: LoginShared, want: true},
		{mode: LoginInSession, want: false},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			runtime := &startAskingSandbox{forwardingSandbox: forwardingSandbox{newFakeSandbox()}}
			svc := newServiceWithRuntime(t, db.OpenForTest(t), runtime)
			runtime.svc = svc
			svc.sharedLoginStore = NewSharedLoginStore(db.OpenForTest(t))

			if _, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", LoginMode: tc.mode}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if runtime.err != nil || runtime.shares != tc.want {
				t.Errorf("asked while booting: %v, %v; want %v and no error", runtime.shares, runtime.err, tc.want)
			}
		})
	}
}

func TestSetLoginMode_StopsTheOldAgentFirst(t *testing.T) {
	cases := []struct {
		name         string
		execs        []execResponse
		wantErr      bool
		wantMode     LoginMode
		wantNewAgent bool
	}{
		{
			name:         "the old agent stops",
			execs:        []execResponse{{exit: 0}, {exit: 1}, {exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}},
			wantMode:     LoginWithLLMProxy,
			wantNewAgent: true,
		},
		{
			name:     "the old agent is still running after the kill",
			execs:    []execResponse{{exit: 1}, {exit: 0}},
			wantErr:  true,
			wantMode: LoginInSession,
		},
		{
			// The old agent is gone but the check could not say so: it comes back the way it was.
			name:         "the kill worked but the check failed",
			execs:        []execResponse{{exit: 0}, {err: errTmuxBroken}, {exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}},
			wantErr:      true,
			wantMode:     LoginInSession,
			wantNewAgent: true,
		},
		{
			name:     "tmux cannot be reached to check",
			execs:    []execResponse{{err: errTmuxBroken}, {err: errTmuxBroken}},
			wantErr:  true,
			wantMode: LoginInSession,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, fake, _, _ := newTestService(t)
			svc.SetLLMProxyURL("http://100.75.35.244:9000")
			sess := mustRunning(t, svc)
			fake.execResponses = append([]execResponse(nil), tc.execs...)
			fake.execCalls = nil

			_, err := svc.SetLoginMode(ctx, sess.ID, LoginWithLLMProxy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			after, _ := svc.Get(ctx, sess.ID)
			if after.LoginMode != tc.wantMode {
				t.Errorf("login mode = %q, want %q: the row must match the agent that runs", after.LoginMode, tc.wantMode)
			}
			startedOne := false
			for _, call := range fake.execCalls {
				if len(call) > 1 && call[0] == "tmux" && call[1] == "new-session" {
					startedOne = true
				}
			}
			if startedOne != tc.wantNewAgent {
				t.Errorf("started a new agent: %v, want %v (calls %v)", startedOne, tc.wantNewAgent, fake.execCalls)
			}
		})
	}
}

func TestService_lockAgent(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	unlock, err := svc.lockAgent(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("lockAgent: %v", err)
	}

	cases := []struct {
		name      string
		sessionID string
		wantErr   bool
	}{
		{name: "the same session waits until it gives up", sessionID: "session-1", wantErr: true},
		{name: "another session does not wait", sessionID: "session-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			other, err := svc.lockAgent(ctx, tc.sessionID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if err == nil {
				other()
			}
		})
	}

	unlock()
	again, err := svc.lockAgent(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("lockAgent after unlock: %v", err)
	}
	again()
}

func TestStartAgentSession_WaitsForALoginChange(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	sess := mustRunning(t, svc)
	unlock, err := svc.lockAgent(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := svc.startAgentSession(ctx, sess, "/workspace"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wait for the lock until its context ends", err)
	}
}

func TestSetLoginMode_WaitsForTheSharedLogin(t *testing.T) {
	tokenCheck := []string{"test", "-s", "/root/.claude/.credentials.json"}
	stopped := []execResponse{{exit: 0}, {exit: 1}}                       // kill-session, then has-session: gone
	started := []execResponse{{exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}} // has-session, mkdir, new-session, set-option
	cases := []struct {
		name            string
		loggedIn        bool
		execs           []execResponse
		wantTokenChecks int
	}{
		{
			name:            "the login arrives on the third look",
			loggedIn:        true,
			execs:           append(append(append([]execResponse(nil), stopped...), execResponse{exit: 1}, execResponse{exit: 1}, execResponse{exit: 0}), started...),
			wantTokenChecks: 3,
		},
		{
			name:  "nobody has logged in, so nothing to wait for",
			execs: append(append([]execResponse(nil), stopped...), started...),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			runtime := forwardingSandbox{newFakeSandbox()}
			database := db.OpenForTest(t)
			svc := newServiceWithRuntime(t, database, runtime)
			svc.SetLLMProxyURL("http://100.75.35.244:9000")
			store := NewSharedLoginStore(database)
			if tc.loggedIn {
				_ = store.Save("claude", sharedlogin.Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1"})
			}
			svc.SetSharedLogins(sharedlogin.NewKeeper(store, map[string]sharedlogin.Provider{"claude": staticProvider{}}), store)
			svc.sharedLoginWaitEvery = time.Millisecond
			sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", LoginMode: LoginWithLLMProxy})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			runtime.execResponses = append([]execResponse(nil), tc.execs...)
			runtime.execCalls = nil

			if _, err := svc.SetLoginMode(ctx, sess.ID, LoginShared); err != nil {
				t.Fatalf("SetLoginMode: %v", err)
			}
			checks, lastCheck, newSession := 0, -1, -1
			for i, call := range runtime.execCalls {
				if slices.Equal(call, tokenCheck) {
					checks, lastCheck = checks+1, i
				}
				if len(call) > 1 && call[1] == "new-session" {
					newSession = i
				}
			}
			if checks != tc.wantTokenChecks {
				t.Errorf("looked for the login %d times, want %d: %v", checks, tc.wantTokenChecks, runtime.execCalls)
			}
			if newSession < 0 || newSession < lastCheck {
				t.Errorf("the agent started at call %d, want it after the login arrived at call %d", newSession, lastCheck)
			}
		})
	}
}

func TestSetLoginMode_StartsTheAgentWhenTheSharedLoginIsLate(t *testing.T) {
	ctx := context.Background()
	runtime := forwardingSandbox{newFakeSandbox()}
	database := db.OpenForTest(t)
	svc := newServiceWithRuntime(t, database, runtime)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	store := NewSharedLoginStore(database)
	_ = store.Save("claude", sharedlogin.Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1"})
	svc.SetSharedLogins(sharedlogin.NewKeeper(store, map[string]sharedlogin.Provider{"claude": staticProvider{}}), store)
	svc.sharedLoginWaitLimit, svc.sharedLoginWaitEvery = 20*time.Millisecond, time.Millisecond
	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", LoginMode: LoginWithLLMProxy})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Stopped and gone, then every look for the login fails until the wait gives up.
	runtime.execResponses = []execResponse{{exit: 0}, {exit: 1}}
	runtime.execCalls = nil

	if _, err := svc.SetLoginMode(ctx, sess.ID, LoginShared); err != nil {
		t.Fatalf("SetLoginMode: %v", err)
	}
	last := runtime.execCalls[len(runtime.execCalls)-1]
	if len(last) < 2 || last[0] != "tmux" || last[1] != "has-session" {
		t.Errorf("last call %v, want the agent start's has-session after the wait gave up", last)
	}
}

func TestStopDestroyAndEditSharedFolders_WaitForTheAgentLock(t *testing.T) {
	cases := []struct {
		name string
		call func(svc *Service, ctx context.Context, id string) error
	}{
		{name: "stop", call: (*Service).Stop},
		{name: "destroy", call: (*Service).Destroy},
		{name: "edit folders", call: func(svc *Service, ctx context.Context, id string) error {
			_, err := svc.EditSharedFolders(ctx, id, nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			unlock, err := svc.lockAgent(context.Background(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := tc.call(svc, ctx, sess.ID); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want it to wait while a login change holds the agent", err)
			}
		})
	}
}

// cancelingSandbox cancels the request when a given tmux command runs, then refuses to exec.
type cancelingSandbox struct {
	*fakeSandbox
	cancelOn string
	cancel   context.CancelFunc
}

func (c *cancelingSandbox) Exec(ctx context.Context, sandboxID sandbox.ID, spec sandbox.ExecSpec) (sandbox.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proc, err := c.fakeSandbox.Exec(ctx, sandboxID, spec)
	if len(spec.Cmd) > 1 && spec.Cmd[0] == "tmux" && spec.Cmd[1] == c.cancelOn {
		c.cancel()
	}
	return proc, err
}

func TestSetLoginMode_FinishesWhenTheRequestGivesUp(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &cancelingSandbox{fakeSandbox: newFakeSandbox(), cancelOn: "kill-session", cancel: cancel}
	svc := newServiceWithRuntime(t, db.OpenForTest(t), runtime)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runtime.execResponses = []execResponse{{exit: 0}, {exit: 1}, {exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}}

	if _, err := svc.SetLoginMode(requestCtx, sess.ID, LoginWithLLMProxy); err != nil {
		t.Fatalf("SetLoginMode: %v, want it to finish after the request gave up", err)
	}
	after, _ := svc.Get(context.Background(), sess.ID)
	if after.LoginMode != LoginWithLLMProxy {
		t.Errorf("login mode = %q, want the change to have finished", after.LoginMode)
	}
}

func TestPatch_RefusedLoginModeLeavesTheNameAlone(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantName   string
	}{
		// The fake runtime is no VM, so it cannot share.
		{name: "a mode the session cannot use", body: `{"name": "renamed", "login_mode": "shared"}`, wantStatus: http.StatusBadRequest, wantName: "keep-me"},
		{name: "the mode it already has", body: `{"name": "renamed", "login_mode": "in_session"}`, wantStatus: http.StatusOK, wantName: "renamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _, _, _ := newTestService(t)
			sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "keep-me"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mux := http.NewServeMux()
			svc.AttachRoutes(mux)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sess.ID, strings.NewReader(tc.body)))
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantStatus, w.Body)
			}
			after, _ := svc.Get(ctx, sess.ID)
			if after.Name != tc.wantName {
				t.Errorf("name = %q, want %q: a refused patch must change nothing", after.Name, tc.wantName)
			}
		})
	}
}

func TestSetLoginMode_RefusedWhileStarting(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newTestService(t)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	sess := mustRunning(t, svc)
	done, ok := svc.beginStarting(sess.ID)
	if !ok {
		t.Fatal("could not mark the session starting")
	}
	defer done()

	if _, err := svc.SetLoginMode(ctx, sess.ID, LoginWithLLMProxy); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want it refused while the machine starts", err)
	}
	if after, _ := svc.repo.Get(ctx, placeholderOwner, sess.ID); after.LoginMode != sess.LoginMode {
		t.Errorf("login mode = %q, want it unchanged", after.LoginMode)
	}
}

func TestStartAgentSession_UsesTheSavedLoginMode(t *testing.T) {
	ctx := context.Background()
	runtime := forwardingSandbox{newFakeSandbox()}
	svc := newServiceWithRuntime(t, db.OpenForTest(t), runtime)
	svc.SetLLMProxyURL("http://100.75.35.244:9000")
	stale, err := svc.Create(ctx, CreateRequest{Agent: "claude", LoginMode: LoginInSession})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.repo.UpdateLoginMode(ctx, placeholderOwner, stale.ID, LoginWithLLMProxy, stale.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	runtime.execResponses = []execResponse{{exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}} // has-session, mkdir, new-session, set-option
	runtime.execCalls = nil

	if err := svc.startAgentSession(ctx, stale, "/workspace"); err != nil {
		t.Fatalf("startAgentSession: %v", err)
	}
	var launch []string
	for _, call := range runtime.execCalls {
		if len(call) > 1 && call[1] == "new-session" {
			launch = call
		}
	}
	if !slices.Contains(launch, "ANTHROPIC_BASE_URL=http://127.0.0.1:9100") {
		t.Errorf("the agent started with %v, want the saved mode's LLM proxy address", launch)
	}
}
