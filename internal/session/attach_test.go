package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestValidateTmuxWindowNameSyntax pins the difference between the strict and syntax-only validators:
// attach uses the syntax-only one because "main" is the agent window (a perfectly valid attach
// target) even though it's reserved for *creating* windows.
func TestValidateTmuxWindowNameSyntax(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "main is allowed (unlike strict validator)", input: "main", wantErr: nil},
		{name: "lowercase shell", input: "shell", wantErr: nil},
		{name: "with hyphen and digits", input: "shell-2", wantErr: nil},
		{name: "max length", input: strings.Repeat("a", 32), wantErr: nil},

		{name: "empty", input: "", wantErr: ErrTmuxWindowNameInvalid},
		{name: "too long", input: strings.Repeat("a", 33), wantErr: ErrTmuxWindowNameInvalid},
		{name: "uppercase", input: "Shell", wantErr: ErrTmuxWindowNameInvalid},
		{name: "underscore", input: "my_shell", wantErr: ErrTmuxWindowNameInvalid},
		{name: "space", input: "my shell", wantErr: ErrTmuxWindowNameInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateTmuxWindowNameSyntax(tc.input)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("validateTmuxWindowNameSyntax(%q): got %v, want errors.Is %v", tc.input, got, tc.wantErr)
			}
		})
	}
}

// newAttachTestServer wires Service routes into an httptest server,
// and returns a `done` channel that closes after each request handler
// returns. Tests use `done` to wait for the deferred view-session
// cleanup to run before asserting on fake.execCalls.
func newAttachTestServer(t *testing.T, svc *Service) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{}, 1)
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, done
}

// wsURL converts an http(s)://host base into a ws(s):// URL with the
// given path appended.
func wsURL(t *testing.T, baseURL, path string) string {
	t.Helper()
	if strings.HasPrefix(baseURL, "https://") {
		return "wss://" + strings.TrimPrefix(baseURL, "https://") + path
	}
	return "ws://" + strings.TrimPrefix(baseURL, "http://") + path
}

// TestHandleAttach_PreUpgradeValidation covers the three reject paths that happen BEFORE the
// WebSocket upgrade; these are the cheap "fail with a clean HTTP error" cases. After upgrade, errors
// are awkward close frames, so we want to keep these before-upgrade branches well-tested.
func TestHandleAttach_PreUpgradeValidation(t *testing.T) {
	tests := []struct {
		name       string
		setupID    func(t *testing.T, svc *Service) string
		query      string // appended after `?`. Empty string means no query
		wantStatus int
	}{
		{
			name: "unknown session id → 404",
			setupID: func(t *testing.T, svc *Service) string {
				return "no-such-id"
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "stopped session → 409",
			setupID: func(t *testing.T, svc *Service) string {
				sess := mustRunning(t, svc)
				if err := svc.Stop(context.Background(), sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
				return sess.ID
			},
			wantStatus: http.StatusConflict,
		},
		{
			name: "invalid tmux_window syntax → 400",
			setupID: func(t *testing.T, svc *Service) string {
				return mustRunning(t, svc).ID
			},
			query:      "tmux_window=Has%20Space",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			id := tc.setupID(t, svc)

			ts, _ := newAttachTestServer(t, svc)
			url := ts.URL + "/api/sessions/" + id + "/attach"
			if tc.query != "" {
				url += "?" + tc.query
			}
			resp, err := http.Get(url)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// TestHandleAttach_ViewSessionLifecycle exercises the per-attach view session: the substrate for
// Split mode (per-client window views) and the fix for tmux clients piling up after each toggle.
// Asserts:
//
//  1. A `view-<uuid>` session is created as a member of main's group.
//  2. The attach exec targets that view session.
//  3. When tmux_window is supplied, the chained `select-window` only
//     focuses the new view (per-client focus, not main's).
//  4. On disconnect the view session is killed; the cleanup defer
//     fires regardless of how the bridge tears down.
func TestHandleAttach_ViewSessionLifecycle(t *testing.T) {
	tests := []struct {
		name           string
		tmuxWindow     string // "" → no ?tmux_window= query
		wantAttachLen  int    // expected len of attach exec cmd
		wantSelectArgs []string
	}{
		{
			name:          "no ?tmux_window= → plain attach to view",
			tmuxWindow:    "",
			wantAttachLen: 5, // tmux -u attach -t view-XXX
		},
		{
			name:           "?tmux_window=main → chained select-window to view:main",
			tmuxWindow:     "main",
			wantAttachLen:  9, // tmux -u attach -t view-XXX ; select-window -t view-XXX:main
			wantSelectArgs: []string{";", "select-window", "-t"},
		},
		{
			name:           "?tmux_window=shell → chained select-window to view:shell",
			tmuxWindow:     "shell",
			wantAttachLen:  9,
			wantSelectArgs: []string{";", "select-window", "-t"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)

			// Three scripted Exec responses, in call order:
			//   1. new-session (setup)
			//   2. attach      (the bridge: empty stdout EOFs fast, tearing the bridge down)
			//   3. kill-session (deferred cleanup)
			fake.execResponses = []execResponse{
				{exit: 0},
				{exit: 0},
				{exit: 0},
			}
			fake.execCalls = nil

			ts, done := newAttachTestServer(t, svc)
			url := wsURL(t, ts.URL, "/api/sessions/"+sess.ID+"/attach")
			if tc.tmuxWindow != "" {
				url += "?tmux_window=" + tc.tmuxWindow
			}

			dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer dialCancel()
			ws, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
				Subprotocols: []string{AttachSubprotocol},
			})
			if err != nil {
				t.Fatalf("ws dial: %v", err)
			}
			// Close immediately. Server-side: bridge's stdout reader
			// EOFs on the empty scripted process, cancelling the
			// bridge ctx, which ends the goroutines and runs the
			// deferred cleanup.
			_ = ws.Close(websocket.StatusNormalClosure, "")

			// Wait for handler to fully return so the deferred
			// cleanup has run and execCalls reflects the kill.
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handleAttach did not return within 3s")
			}

			if len(fake.execCalls) != 3 {
				t.Fatalf("expected 3 exec calls (new/attach/kill); got %d: %v", len(fake.execCalls), fake.execCalls)
			}

			// Call 0: new-session as a member of main's group.
			gotNew := fake.execCalls[0]
			wantNewPrefix := []string{"tmux", "new-session", "-d", "-s"}
			if !sliceHasPrefix(gotNew, wantNewPrefix) {
				t.Errorf("new-session cmd prefix: got %v, want prefix %v", gotNew, wantNewPrefix)
			}
			if len(gotNew) < 7 {
				t.Fatalf("new-session cmd too short: %v", gotNew)
			}
			viewName := gotNew[4]
			if !strings.HasPrefix(viewName, "view-") {
				t.Errorf("view session name: got %q, want prefix %q", viewName, "view-")
			}
			if gotNew[5] != "-t" || gotNew[6] != AgentTmuxWindowName {
				t.Errorf("new-session group target: got %v, want -t %s", gotNew[5:], AgentTmuxWindowName)
			}

			// Call 1: attach to the view session, optionally chained
			// with select-window.
			gotAttach := fake.execCalls[1]
			if len(gotAttach) != tc.wantAttachLen {
				t.Errorf("attach cmd length: got %d, want %d (%v)", len(gotAttach), tc.wantAttachLen, gotAttach)
			}
			// The -u between tmux and attach forces UTF-8 client mode; fixes the
			// `_` substitution of high-bit chars (❯ ← •) on Alpine where no locale
			// is set. Drift here without test catching it would re-break the TUI.
			wantAttachHead := []string{"tmux", "-u", "attach", "-t", viewName}
			if !sliceHasPrefix(gotAttach, wantAttachHead) {
				t.Errorf("attach cmd head: got %v, want prefix %v", gotAttach, wantAttachHead)
			}
			if tc.tmuxWindow != "" {
				wantTarget := viewName + ":" + tc.tmuxWindow
				if len(gotAttach) < 9 || gotAttach[8] != wantTarget {
					t.Errorf("select-window target: got %v, want trailing %s", gotAttach, wantTarget)
				}
				if !sliceHasPrefix(gotAttach[5:], tc.wantSelectArgs) {
					t.Errorf("select-window middle: got %v, want %v after attach head", gotAttach[5:], tc.wantSelectArgs)
				}
			}

			// Call 2: kill-session on the same view name. Confirms
			// the deferred cleanup wired through the right name and
			// will run on every return path.
			gotKill := fake.execCalls[2]
			wantKill := []string{"tmux", "kill-session", "-t", viewName}
			if !slicesEqual(gotKill, wantKill) {
				t.Errorf("kill cmd: got %v, want %v", gotKill, wantKill)
			}
		})
	}
}

// TestHandleAttach_ViewSessionSetupFailure asserts the 500 path: if
// the per-attach view session can't be created, we bail BEFORE the
// WS upgrade (no awkward close frame) and DO NOT run the cleanup
// defer for a session that was never created.
func TestHandleAttach_ViewSessionSetupFailure(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	sess := mustRunning(t, svc)

	fake.execResponses = []execResponse{
		{exit: 1, stderr: "tmux: server lost"},
	}
	fake.execCalls = nil

	ts, _ := newAttachTestServer(t, svc)
	resp, err := http.Get(ts.URL + "/api/sessions/" + sess.ID + "/attach")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if len(fake.execCalls) != 1 {
		t.Errorf("expected exactly 1 exec call (the failed new-session); got %d: %v",
			len(fake.execCalls), fake.execCalls)
	}
	// No attach exec, no kill; the failed new-session means there's
	// nothing to clean up. If a future change accidentally schedules
	// cleanup before the success check, this guards against the
	// spurious kill-session.
}

// sliceHasPrefix reports whether s starts with prefix.
func sliceHasPrefix(s, prefix []string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i, v := range prefix {
		if s[i] != v {
			return false
		}
	}
	return true
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
