package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ---- pure helper tests ---------------------------------------------------

func TestParseTmuxWindowsList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want *TmuxWindowsResponse
	}{
		{
			name: "empty output",
			in:   "",
			want: &TmuxWindowsResponse{Windows: []TmuxWindow{}, Active: ""},
		},
		{
			name: "single active window",
			in:   "0 main 1\n",
			want: &TmuxWindowsResponse{
				Windows: []TmuxWindow{{Name: "main", Index: 0}},
				Active:  "main",
			},
		},
		{
			name: "multiple windows, active in middle",
			in:   "0 main 0\n1 shell 1\n2 shell-2 0\n",
			want: &TmuxWindowsResponse{
				Windows: []TmuxWindow{
					{Name: "main", Index: 0},
					{Name: "shell", Index: 1},
					{Name: "shell-2", Index: 2},
				},
				Active: "shell",
			},
		},
		{
			name: "malformed line skipped",
			in:   "0 main 1\nbroken-line\n2 shell 0\n",
			want: &TmuxWindowsResponse{
				Windows: []TmuxWindow{
					{Name: "main", Index: 0},
					{Name: "shell", Index: 2},
				},
				Active: "main",
			},
		},
		{
			name: "non-numeric index skipped",
			in:   "abc main 1\n",
			want: &TmuxWindowsResponse{Windows: []TmuxWindow{}, Active: ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTmuxWindowsList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTmuxWindowsList(%q):\n got  %#v\n want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateTmuxWindowName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "valid lowercase", input: "shell", wantErr: nil},
		{name: "valid with hyphen", input: "shell-2", wantErr: nil},
		{name: "valid with digits", input: "abc-123", wantErr: nil},
		{name: "valid max length", input: strings.Repeat("a", 32), wantErr: nil},

		{name: "empty", input: "", wantErr: ErrTmuxWindowNameInvalid},
		{name: "too long", input: strings.Repeat("a", 33), wantErr: ErrTmuxWindowNameInvalid},
		{name: "uppercase", input: "Shell", wantErr: ErrTmuxWindowNameInvalid},
		{name: "underscore", input: "my_shell", wantErr: ErrTmuxWindowNameInvalid},
		{name: "space", input: "my shell", wantErr: ErrTmuxWindowNameInvalid},
		{name: "dot", input: "shell.1", wantErr: ErrTmuxWindowNameInvalid},

		{name: "reserved main", input: "main", wantErr: ErrTmuxWindowNameReserved},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateTmuxWindowName(tc.input)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("validateTmuxWindowName(%q): got %v, want errors.Is %v", tc.input, got, tc.wantErr)
			}
		})
	}
}

func TestPickNextShellName(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{name: "empty", existing: nil, want: "shell"},
		{name: "shell taken", existing: []string{"shell"}, want: "shell-2"},
		{name: "shell and 2 taken", existing: []string{"shell", "shell-2"}, want: "shell-3"},
		{name: "only shell-2 taken, bare 'shell' wins", existing: []string{"shell-2"}, want: "shell"},
		{name: "fills gap when shell + shell-2 + shell-4", existing: []string{"shell", "shell-2", "shell-4"}, want: "shell-3"},
		{name: "ignores unrelated names", existing: []string{"main", "agent", "custom"}, want: "shell"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			existing := make(map[string]bool, len(tc.existing))
			for _, n := range tc.existing {
				existing[n] = true
			}
			got := pickNextShellName(existing)
			if got != tc.want {
				t.Errorf("pickNextShellName(%v): got %q, want %q", tc.existing, got, tc.want)
			}
		})
	}
}

// ---- service-level tests with scripted fake Exec ------------------------

// mustRunning returns a running session via the standard create path;
// callers can then attach execResponses to the fake to script
// TmuxWindow* behaviour.
func mustRunning(t *testing.T, svc *Service) *Session {
	t.Helper()
	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("setup Create: %v", err)
	}
	return sess
}

func TestService_ListTmuxWindows(t *testing.T) {
	tests := []struct {
		name        string
		setupExecs  []execResponse
		stopFirst   bool
		wantErrIs   error
		wantWindows []TmuxWindow
		wantActive  string
	}{
		{
			name: "happy path: agent + shell",
			setupExecs: []execResponse{
				{stdout: "0 main 1\n1 shell 0\n", exit: 0},
			},
			wantWindows: []TmuxWindow{
				{Name: "main", Index: 0},
				{Name: "shell", Index: 1},
			},
			wantActive: "main",
		},
		{
			name:      "stopped session → ErrSessionNotRunning",
			stopFirst: true,
			wantErrIs: ErrSessionNotRunning,
		},
		{
			name: "exec failure surfaces",
			setupExecs: []execResponse{
				{exit: 1, stderr: "tmux: no server"},
			},
			wantErrIs: nil, // wrapped error; just expect non-nil
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			if tc.stopFirst {
				if err := svc.Stop(context.Background(), sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
			}
			fake.execResponses = tc.setupExecs

			resp, err := svc.ListTmuxWindows(context.Background(), sess.ID)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("err: got %v, want errors.Is %v", err, tc.wantErrIs)
				}
				return
			}
			if tc.name == "exec failure surfaces" {
				if err == nil {
					t.Error("want non-nil error from exec failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("ListTmuxWindows: %v", err)
			}
			if !reflect.DeepEqual(resp.Windows, tc.wantWindows) {
				t.Errorf("windows: got %#v, want %#v", resp.Windows, tc.wantWindows)
			}
			if resp.Active != tc.wantActive {
				t.Errorf("active: got %q, want %q", resp.Active, tc.wantActive)
			}
		})
	}
}

func TestService_CreateTmuxWindow(t *testing.T) {
	tests := []struct {
		name         string
		req          CreateTmuxWindowRequest
		setupExecs   []execResponse
		wantErrIs    error
		wantName     string
		wantIndex    int
		wantCmdCount int // expected number of Exec calls
	}{
		{
			name: "auto-name when blank picks 'shell'",
			req:  CreateTmuxWindowRequest{},
			setupExecs: []execResponse{
				{stdout: "0 main 1\n", exit: 0}, // ListTmuxWindows
				{stdout: "1\n", exit: 0},        // new-window
			},
			wantName:     "shell",
			wantIndex:    1,
			wantCmdCount: 2,
		},
		{
			name: "auto-name picks shell-2 when shell taken",
			req:  CreateTmuxWindowRequest{},
			setupExecs: []execResponse{
				{stdout: "0 main 1\n1 shell 0\n", exit: 0},
				{stdout: "2\n", exit: 0},
			},
			wantName:     "shell-2",
			wantIndex:    2,
			wantCmdCount: 2,
		},
		{
			name: "explicit valid name",
			req:  CreateTmuxWindowRequest{Name: "build"},
			setupExecs: []execResponse{
				{stdout: "0 main 1\n", exit: 0},
				{stdout: "1\n", exit: 0},
			},
			wantName:     "build",
			wantIndex:    1,
			wantCmdCount: 2,
		},
		{
			name:         "invalid name → no exec",
			req:          CreateTmuxWindowRequest{Name: "Has Space"},
			wantErrIs:    ErrTmuxWindowNameInvalid,
			wantCmdCount: 0,
		},
		{
			name:         "reserved name → no exec",
			req:          CreateTmuxWindowRequest{Name: "main"},
			wantErrIs:    ErrTmuxWindowNameReserved,
			wantCmdCount: 0,
		},
		{
			name: "name already exists → ErrTmuxWindowExists (after list)",
			req:  CreateTmuxWindowRequest{Name: "shell"},
			setupExecs: []execResponse{
				{stdout: "0 main 1\n1 shell 0\n", exit: 0},
			},
			wantErrIs:    ErrTmuxWindowExists,
			wantCmdCount: 1, // list, but not new-window
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			fake.execResponses = tc.setupExecs
			fake.execCalls = nil // reset

			win, err := svc.CreateTmuxWindow(context.Background(), sess.ID, tc.req)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("err: got %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if len(fake.execCalls) != tc.wantCmdCount {
					t.Errorf("exec call count: got %d, want %d (%v)", len(fake.execCalls), tc.wantCmdCount, fake.execCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateTmuxWindow: %v", err)
			}
			if win.Name != tc.wantName {
				t.Errorf("name: got %q, want %q", win.Name, tc.wantName)
			}
			if win.Index != tc.wantIndex {
				t.Errorf("index: got %d, want %d", win.Index, tc.wantIndex)
			}
			if len(fake.execCalls) != tc.wantCmdCount {
				t.Errorf("exec call count: got %d, want %d", len(fake.execCalls), tc.wantCmdCount)
			}
		})
	}
}

func TestService_SelectTmuxWindow(t *testing.T) {
	tests := []struct {
		name       string
		windowName string
		setupExec  execResponse
		wantErrIs  error
	}{
		{
			name:       "happy path",
			windowName: "shell",
			setupExec:  execResponse{exit: 0},
			wantErrIs:  nil,
		},
		{
			name:       "window not found",
			windowName: "ghost",
			setupExec:  execResponse{exit: 1, stderr: "can't find window: main:ghost"},
			wantErrIs:  ErrTmuxWindowNotFound,
		},
		{
			name:       "tmux returns 'no such target' phrasing",
			windowName: "ghost",
			setupExec:  execResponse{exit: 1, stderr: "no such window"},
			wantErrIs:  ErrTmuxWindowNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			fake.execResponses = []execResponse{tc.setupExec}

			err := svc.SelectTmuxWindow(context.Background(), sess.ID, tc.windowName)
			if tc.wantErrIs == nil {
				if err != nil {
					t.Errorf("want nil, got %v", err)
				}
			} else {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("err: got %v, want errors.Is %v", err, tc.wantErrIs)
				}
			}
		})
	}
}

func TestService_DeleteTmuxWindow(t *testing.T) {
	tests := []struct {
		name         string
		windowName   string
		setupExec    *execResponse // nil → no exec expected
		wantErrIs    error
		wantCmdCount int
	}{
		{
			name:         "happy path",
			windowName:   "shell",
			setupExec:    &execResponse{exit: 0},
			wantCmdCount: 1,
		},
		{
			name:         "refuses to delete 'main', short-circuits before exec",
			windowName:   "main",
			setupExec:    nil,
			wantErrIs:    ErrTmuxWindowNameReserved,
			wantCmdCount: 0,
		},
		{
			name:         "window not found",
			windowName:   "ghost",
			setupExec:    &execResponse{exit: 1, stderr: "can't find window"},
			wantErrIs:    ErrTmuxWindowNotFound,
			wantCmdCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			sess := mustRunning(t, svc)
			if tc.setupExec != nil {
				fake.execResponses = []execResponse{*tc.setupExec}
			}
			fake.execCalls = nil

			err := svc.DeleteTmuxWindow(context.Background(), sess.ID, tc.windowName)
			if tc.wantErrIs == nil {
				if err != nil {
					t.Errorf("want nil, got %v", err)
				}
			} else {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("err: got %v, want errors.Is %v", err, tc.wantErrIs)
				}
			}
			if len(fake.execCalls) != tc.wantCmdCount {
				t.Errorf("exec call count: got %d, want %d", len(fake.execCalls), tc.wantCmdCount)
			}
		})
	}
}

// A tmux call that fails while the entrypoint is still installing must say the session is starting, not
// surface a raw exec status the user cannot act on.
func TestTmuxFailureDuringSetup(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	ctx := context.Background()
	sess := mustCreate(t, svc, "claude")

	// tmux is not there yet, so the call fails the way a real one does mid setup. The bootstrap probe
	// that follows also finds nothing, which is exactly what "still installing" looks like.
	fake.execResponses = append(fake.execResponses, execResponse{exit: -1})

	_, err := svc.ListTmuxWindows(ctx, sess.ID)
	if !errors.Is(err, ErrSessionStarting) {
		t.Fatalf("error = %v, want ErrSessionStarting", err)
	}
}
