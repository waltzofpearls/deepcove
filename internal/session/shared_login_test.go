package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// staticProvider refreshes a login into itself. owners is which account each token belongs to.
type staticProvider struct{ owners map[string]string }

func (staticProvider) Refresh(_ context.Context, login sharedlogin.Login) (sharedlogin.Login, error) {
	return login, nil
}

func (p staticProvider) Account(_ context.Context, accessToken string) (sharedlogin.Account, error) {
	owner, ok := p.owners[accessToken]
	if !ok {
		return sharedlogin.Account{}, errors.New("the vendor does not know that token")
	}
	return sharedlogin.Account{ID: owner}, nil
}

func TestSharedLoginEndpoints(t *testing.T) {
	waiting := sharedlogin.OtherAccounts{Waiting: &sharedlogin.WaitingLogin{
		Login:     sharedlogin.Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2", AccountName: "work@example.com"},
		SandboxID: "machine-1",
	}}
	cases := []struct {
		name          string
		noKeeper      bool
		others        sharedlogin.OtherAccounts
		method        string
		path          string
		body          string
		wantStatus    int
		wantShared    string
		wantDefault   LoginMode
		wantHasKey    bool
		wantKeyEnding string
	}{
		{name: "status", method: http.MethodGet, path: "/api/shared-logins/claude", wantStatus: http.StatusOK, wantShared: "a", wantDefault: LoginShared},
		{name: "status of an agent that cannot share", method: http.MethodGet, path: "/api/shared-logins/codex", wantStatus: http.StatusNotFound, wantShared: "a"},
		{name: "status on a node that shares nothing", noKeeper: true, method: http.MethodGet, path: "/api/shared-logins/claude", wantStatus: http.StatusNotFound, wantShared: "a"},
		{name: "switch", others: waiting, method: http.MethodPost, path: "/api/shared-logins/claude/switch", wantStatus: http.StatusOK, wantShared: "b"},
		{name: "keep", others: waiting, method: http.MethodPost, path: "/api/shared-logins/claude/keep", wantStatus: http.StatusOK, wantShared: "a"},
		{name: "switch with nothing waiting", method: http.MethodPost, path: "/api/shared-logins/claude/switch", wantStatus: http.StatusConflict, wantShared: "a"},
		{name: "change the default", method: http.MethodPut, path: "/api/shared-logins/claude", body: `{"default_login_mode":"in_session"}`, wantStatus: http.StatusOK, wantShared: "a", wantDefault: LoginInSession},
		{name: "a default the agent does not know", method: http.MethodPut, path: "/api/shared-logins/claude", body: `{"default_login_mode":"magic"}`, wantStatus: http.StatusBadRequest, wantShared: "a"},
		{name: "save an API key, only its ending comes back", method: http.MethodPut, path: "/api/shared-logins/claude", body: `{"api_key":" sk-ant-secret-wxyz "}`, wantStatus: http.StatusOK, wantShared: "a", wantDefault: LoginShared, wantHasKey: true, wantKeyEnding: "wxyz"},
		{name: "a key too short to show an ending is still saved", method: http.MethodPut, path: "/api/shared-logins/claude", body: `{"api_key":"abcd"}`, wantStatus: http.StatusOK, wantShared: "a", wantDefault: LoginShared, wantHasKey: true},
		{name: "settings of an agent that cannot share", method: http.MethodPut, path: "/api/shared-logins/codex", body: `{"default_login_mode":"in_session"}`, wantStatus: http.StatusNotFound, wantShared: "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, database := newTestService(t)
			store := NewSharedLoginStore(database)
			_ = store.Save("claude", sharedlogin.Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1"})
			_ = store.SaveOtherAccounts("claude", tc.others)
			if !tc.noKeeper {
				svc.SetSharedLogins(sharedlogin.NewKeeper(store, map[string]sharedlogin.Provider{"claude": staticProvider{owners: map[string]string{"b": "account-2"}}}), store)
			}
			mux := http.NewServeMux()
			svc.AttachRoutes(mux)

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body)
			}
			if shared, _, _ := store.Load("claude"); shared.AccessToken != tc.wantShared {
				t.Errorf("shared access token = %q, want %q", shared.AccessToken, tc.wantShared)
			}
			if tc.wantStatus == http.StatusOK && tc.method != http.MethodPost {
				var view sharedLoginView
				_ = json.Unmarshal(w.Body.Bytes(), &view)
				if view.DefaultLoginMode != tc.wantDefault || view.HasAPIKey != tc.wantHasKey || view.APIKeyEnding != tc.wantKeyEnding {
					t.Errorf("default %q has key %v ending %q, want %q %v %q",
						view.DefaultLoginMode, view.HasAPIKey, view.APIKeyEnding, tc.wantDefault, tc.wantHasKey, tc.wantKeyEnding)
				}
				if strings.Contains(w.Body.String(), "secret") {
					t.Error("the API key left the node")
				}
			}
		})
	}
}

func TestService_sharesLogin(t *testing.T) {
	cases := []struct {
		name        string
		defaultMode LoginMode
		mode        LoginMode
		want        bool
	}{
		{name: "shared", defaultMode: LoginInSession, mode: LoginShared, want: true},
		{name: "its own login", defaultMode: LoginShared, mode: LoginInSession, want: false},
		{name: "the API key", defaultMode: LoginShared, mode: LoginWithAPIKey, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _, _, database := newTestService(t)
			store := NewSharedLoginStore(database)
			_ = store.SetDefaultLoginMode("claude", tc.defaultMode)
			svc.SetSharedLogins(sharedlogin.NewKeeper(store, map[string]sharedlogin.Provider{"claude": staticProvider{}}), store)
			sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			_ = svc.repo.UpdateLoginMode(ctx, placeholderOwner, sess.ID, tc.mode, sess.UpdatedAt)

			if got, err := svc.sharesLogin("claude", sess.ID); err != nil || got != tc.want {
				t.Errorf("sharesLogin = %v, %v; want %v", got, err, tc.want)
			}
			if got, err := svc.sharesLogin("claude", workspaceVMHomeDiskID); err != nil || got != (tc.defaultMode == LoginShared) {
				t.Errorf("workspace VM sharesLogin = %v, %v; want it to follow the default %q", got, err, tc.defaultMode)
			}
		})
	}
}
