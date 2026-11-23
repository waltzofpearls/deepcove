package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

func TestHandleAttach_RefusesPlainGet(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	sess := mustRunning(t, svc)
	fake.execCalls = nil

	ts, _ := newAttachTestServer(t, svc)
	resp, err := http.Get(ts.URL + "/api/sessions/" + sess.ID + "/attach")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
	if len(fake.execCalls) != 0 {
		t.Errorf("a plain GET ran %d commands in the sandbox, want none: %v", len(fake.execCalls), fake.execCalls)
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

			// Four scripted Exec responses, in call order:
			//   1. has-session (the agent session is already there, so attach only groups onto it)
			//   2. new-session (setup)
			//   3. attach      (the bridge: empty stdout EOFs fast, tearing the bridge down)
			//   4. kill-session (deferred cleanup)
			fake.execResponses = []execResponse{
				{exit: 0},
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

			if len(fake.execCalls) != 4 {
				t.Fatalf("expected 4 exec calls (has/new/attach/kill); got %d: %v", len(fake.execCalls), fake.execCalls)
			}

			// Call 0: the safety net checking the agent's own tmux session is there.
			wantHas := []string{"tmux", "has-session", "-t", tmuxSessionName(sess.ID)}
			if !slicesEqual(fake.execCalls[0], wantHas) {
				t.Errorf("has-session cmd: got %v, want %v", fake.execCalls[0], wantHas)
			}

			// Call 1: new-session as a member of the agent session's group.
			gotNew := fake.execCalls[1]
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
			// Grouped onto THIS session's tmux session, not a machine-wide "main": a machine can host
			// several sessions, and an attach must join the right one.
			wantGroup := tmuxSessionName(sess.ID)
			if gotNew[5] != "-t" || gotNew[6] != wantGroup {
				t.Errorf("new-session group target: got %v, want -t %s", gotNew[5:], wantGroup)
			}

			// Call 2: attach to the view session, optionally chained
			// with select-window.
			gotAttach := fake.execCalls[2]
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

			// Call 3: kill-session on the same view name. Confirms
			// the deferred cleanup wired through the right name and
			// will run on every return path.
			gotKill := fake.execCalls[3]
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
		{exit: 0},                              // has-session: the agent session is already there
		{exit: 1, stderr: "tmux: server lost"}, // new-session for the view: this is the failure
	}
	fake.execCalls = nil

	ts, _ := newAttachTestServer(t, svc)
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/"+sess.ID+"/attach", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if len(fake.execCalls) != 2 {
		t.Errorf("expected 2 exec calls (has-session, the failed new-session); got %d: %v",
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

func TestCloseAttachStreams(t *testing.T) {
	newService := func() *Service { return &Service{logger: slog.New(slog.DiscardHandler)} }

	t.Run("ends a live stream and waits for its cleanup", func(t *testing.T) {
		svc := newService()
		ctx, release, ok := svc.beginAttach(context.Background())
		if !ok {
			t.Fatal("beginAttach refused the first stream")
		}

		cleaned := make(chan struct{})
		go func() {
			<-ctx.Done() // the stop cancels us, like a real stream's bridge returning
			time.Sleep(50 * time.Millisecond)
			close(cleaned)
			release()
		}()

		svc.CloseAttachStreams(2 * time.Second)
		select {
		case <-cleaned:
		default:
			t.Fatal("CloseAttachStreams returned before the stream finished cleaning up")
		}
	})

	t.Run("refuses new streams once stopping", func(t *testing.T) {
		svc := newService()
		svc.CloseAttachStreams(time.Second)
		if _, _, ok := svc.beginAttach(context.Background()); ok {
			t.Fatal("beginAttach accepted a stream during shutdown")
		}
	})

	t.Run("gives up on a stream that never finishes", func(t *testing.T) {
		svc := newService()
		if _, _, ok := svc.beginAttach(context.Background()); !ok {
			t.Fatal("beginAttach refused the first stream")
		}
		start := time.Now()
		svc.CloseAttachStreams(100 * time.Millisecond)
		if waited := time.Since(start); waited > time.Second {
			t.Fatalf("waited %v for a stuck stream, expected to give up at the timeout", waited)
		}
	})
}

func TestBuildLiteralTmuxFormat(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "plain", text: "could not upload a.png", want: "could not upload a.png"},
		{name: "format", text: "#{pane_id}", want: "##{pane_id}"},
		{name: "command", text: "#(rm x).png", want: "##(rm x).png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildLiteralTmuxFormat(tc.text); got != tc.want {
				t.Errorf("buildLiteralTmuxFormat(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestHandleAttach_ShellTab(t *testing.T) {
	cases := []struct {
		name          string
		windows       string
		isNewWindowed bool
	}{
		{"opens the shell tab when there is none", "0 main 1\n", true},
		{"leaves a shell tab that is there", "0 main 1\n1 shell 0\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			// has-session, list-windows, new-window when the tab is missing, new-session, attach, kill-session.
			fake.execResponses = []execResponse{{exit: 0}, {stdout: tc.windows, exit: 0}}
			if tc.isNewWindowed {
				fake.execResponses = append(fake.execResponses, execResponse{stdout: "1\n", exit: 0})
			}
			fake.execResponses = append(fake.execResponses, execResponse{exit: 0}, execResponse{exit: 0}, execResponse{exit: 0})
			fake.execCalls = nil

			ts, done := newAttachTestServer(t, svc)
			url := wsURL(t, ts.URL, "/api/sessions/"+sess.ID+"/attach?shell_tab=true")
			dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer dialCancel()
			ws, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{Subprotocols: []string{AttachSubprotocol}})
			if err != nil {
				t.Fatalf("ws dial: %v", err)
			}
			_ = ws.Close(websocket.StatusNormalClosure, "")
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handleAttach did not return within 3s")
			}

			isNewWindowed := false
			for _, call := range fake.execCalls {
				if strings.Contains(strings.Join(call, " "), "new-window -d -t "+tmuxSessionName(sess.ID)+": -n shell") {
					isNewWindowed = true
				}
			}
			if isNewWindowed != tc.isNewWindowed {
				t.Errorf("opened a shell tab = %v, want %v (calls %v)", isNewWindowed, tc.isNewWindowed, fake.execCalls)
			}
		})
	}
}

func TestSessionView(t *testing.T) {
	cases := []struct {
		name string
		// visibility is what the attach reports, in order, starting with how it opened.
		visibility  []bool
		wantViewed  bool
		wantRecords int32
	}{
		{name: "opened in front", visibility: []bool{true}, wantViewed: true, wantRecords: 1},
		{name: "opened in the background", visibility: []bool{false}},
		{name: "sent to the background", visibility: []bool{true, false}, wantRecords: 2},
		{name: "back in front", visibility: []bool{true, false, true}, wantViewed: true, wantRecords: 3},
		{name: "in front twice", visibility: []bool{true, true}, wantViewed: true, wantRecords: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepoWith()
			svc := NewService(repo, nil)
			view := &sessionView{service: svc, id: "s1"}
			for _, visible := range tc.visibility {
				if err := view.setVisible(t.Context(), visible); err != nil {
					t.Fatal(err)
				}
			}
			if got := svc.isSessionCurrentlyBeingViewed("s1"); got != tc.wantViewed {
				t.Errorf("viewed = %t, want %t", got, tc.wantViewed)
			}
			if got := repo.activityUpdates.Load(); got != tc.wantRecords {
				t.Errorf("activity recorded %d times, want %d", got, tc.wantRecords)
			}
			view.end()
			if svc.isSessionCurrentlyBeingViewed("s1") {
				t.Error("still viewed after the attach ended")
			}
			// A frame the reader was handling as the attach ended must not count the viewer again.
			_ = view.setVisible(t.Context(), true)
			if svc.isSessionCurrentlyBeingViewed("s1") {
				t.Error("viewed again by a visibility change after the attach ended")
			}
		})
	}
}

func TestHandleAttach_RecordsActivity(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantRecorded bool
	}{
		{name: "a terminal on screen", wantRecorded: true},
		{name: "a tab that opened in the background", query: "?hidden=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			before, err := svc.Get(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			// has-session, new-session, attach (empty, so the bridge ends at once), kill-session.
			fake.execResponses = []execResponse{{}, {}, {}, {}}
			ts, done := newAttachTestServer(t, svc)
			ws, _, err := websocket.Dial(t.Context(), wsURL(t, ts.URL, "/api/sessions/"+sess.ID+"/attach"+tc.query),
				&websocket.DialOptions{Subprotocols: []string{AttachSubprotocol}})
			if err != nil {
				t.Fatalf("ws dial: %v", err)
			}
			_ = ws.Close(websocket.StatusNormalClosure, "")
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handleAttach did not return within 3s")
			}
			after, err := svc.Get(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := after.LastActivityAt.After(before.LastActivityAt); got != tc.wantRecorded {
				t.Errorf("activity recorded = %t, want %t", got, tc.wantRecorded)
			}
		})
	}
}

func TestBridge_Visibility(t *testing.T) {
	cases := []struct {
		name string
		// visibility is what the browser sends, in order.
		visibility []bool
	}{
		{name: "sent to the background", visibility: []bool{false}},
		{name: "back in front", visibility: []bool{false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reported := make(chan bool, len(tc.visibility))
			setVisible := func(_ context.Context, visible bool) error {
				reported <- visible
				return nil
			}
			// A terminal whose output never ends, so the bridge stays up while the frames arrive.
			proc := &gatedProcess{scriptedProcess: newScriptedProcess("", "", 0), gate: make(chan struct{}), closed: make(chan struct{})}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				detach := func(context.Context) error {
					return nil
				}
				notify := func(context.Context, string) error {
					return nil
				}
				bridge(r.Context(), ws, proc, slog.New(slog.DiscardHandler), detach, notify, setVisible)
			}))
			defer ts.Close()
			defer func() {
				_ = proc.Close()
			}()
			ws, _, err := websocket.Dial(t.Context(), wsURL(t, ts.URL, "/"), nil)
			if err != nil {
				t.Fatalf("ws dial: %v", err)
			}
			defer func() {
				_ = ws.Close(websocket.StatusNormalClosure, "")
			}()
			for _, visible := range tc.visibility {
				frame, _ := json.Marshal(AttachControlMessage{Type: "visibility", Visible: visible})
				if err := ws.Write(t.Context(), websocket.MessageText, frame); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			for index, want := range tc.visibility {
				select {
				case got := <-reported:
					if got != want {
						t.Errorf("visibility change %d = %t, want %t", index, got, want)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("visibility change %d never reached setVisible", index)
				}
			}
		})
	}
}
