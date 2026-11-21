package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseAgentState(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		source AgentStateSource
		out    string
		want   agentStateRead
	}{
		{
			name:   "claude on a turn",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"busy\"}\n",
			want:   agentStateRead{state: AgentStateWorking},
		},
		{
			name:   "claude waiting on a permission",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"waiting\",\"waitingFor\":\"permission prompt\"}\n",
			want:   agentStateRead{state: AgentStateNeedsYou, detail: "permission prompt"},
		},
		{
			name:   "claude idle with no turn yet",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"idle\"}\n",
			want:   agentStateRead{state: AgentStateIdle},
		},
		{
			name:   "claude finished a turn, on a clock 600s ahead",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1600\nsession_file {\"status\":\"idle\"}\ndone 1590\n",
			want:   agentStateRead{state: AgentStateDone, finishedAt: now.Add(-10 * time.Second)},
		},
		{
			name:   "a background command after the turn is not work",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"shell\"}\ndone 990\n",
			want:   agentStateRead{state: AgentStateDone, finishedAt: now.Add(-10 * time.Second)},
		},
		{
			name:   "claude failed a turn",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"idle\"}\ndone 990\nerror {\"error\":\"server_error\",\"last_assistant_message\":\"API Error: 529\"}\n",
			want:   agentStateRead{state: AgentStateError, detail: "API Error: 529"},
		},
		{
			name:   "a failure from before still shows while claude retries a new turn",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\nsession_file {\"status\":\"busy\"}\nerror {\"error\":\"server_error\"}\n",
			want:   agentStateRead{state: AgentStateWorking},
		},
		{
			name:   "no live claude",
			source: AgentStateFromClaudeSessionFile,
			out:    "now 1000\ndone 990\n",
			want:   agentStateRead{},
		},
		{
			name:   "codex on a turn",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=working:app=codex\nalive\n",
			want:   agentStateRead{state: AgentStateWorking},
		},
		{
			name:   "codex waiting on an approval",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=blocked:app=codex:kind=permission\nalive\n",
			want:   agentStateRead{state: AgentStateNeedsYou, detail: "permission"},
		},
		{
			name:   "codex finished a turn",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=done:app=codex\nalive\n",
			want:   agentStateRead{state: AgentStateDone, finishedAt: now.Add(-10 * time.Second)},
		},
		{
			name:   "codex back at its prompt",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=idle:app=codex\nalive\n",
			want:   agentStateRead{state: AgentStateIdle},
		},
		{
			name:   "pi failed a turn",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=error:app=pi:msg=" + "NTAwIEludGVybmFsIFNlcnZlciBFcnJvcg==" + "\nalive\n",
			want:   agentStateRead{state: AgentStateError, detail: "500 Internal Server Error"},
		},
		{
			name:   "a crashed codex keeps saying working",
			source: AgentStateFromEscapeSequence,
			out:    "now 1000\nescape_sequence 990 state=working:app=codex\n",
			want:   agentStateRead{},
		},
		{
			name:   "nothing printed",
			source: AgentStateFromClaudeSessionFile,
			out:    "",
			want:   agentStateRead{},
		},
		{
			name:   "a done time without the machine's clock",
			source: AgentStateFromClaudeSessionFile,
			out:    "session_file {\"status\":\"idle\"}\ndone 990\n",
			want:   agentStateRead{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAgentState(tc.out, tc.source, now)
			if got != tc.want {
				t.Errorf("parseAgentState() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReportAgentState(t *testing.T) {
	seenAt := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                          string
		status                        Status
		stateRead                     agentStateRead
		isSessionCurrentlyBeingViewed bool
		want                          AgentState
	}{
		{name: "working", status: StatusRunning, stateRead: agentStateRead{state: AgentStateWorking}, want: AgentStateWorking},
		{name: "done after you last looked", status: StatusRunning, stateRead: agentStateRead{state: AgentStateDone, finishedAt: seenAt.Add(time.Minute)}, want: AgentStateDone},
		{name: "done before you last looked", status: StatusRunning, stateRead: agentStateRead{state: AgentStateDone, finishedAt: seenAt.Add(-time.Minute)}, want: AgentStateIdle},
		{name: "done in the second you last looked", status: StatusRunning, stateRead: agentStateRead{state: AgentStateDone, finishedAt: seenAt.Add(800 * time.Millisecond)}, want: AgentStateIdle},
		{name: "done while you are looking", status: StatusRunning, stateRead: agentStateRead{state: AgentStateDone, finishedAt: seenAt.Add(time.Minute)}, isSessionCurrentlyBeingViewed: true, want: AgentStateIdle},
		{name: "a stopped session reads nothing", status: StatusStopped, stateRead: agentStateRead{state: AgentStateWorking}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(nil, nil)
			svc.agentStateCache.bySession = map[string]*cachedAgentStateRead{"s1": {readAt: time.Now(), stateRead: tc.stateRead}}
			if tc.isSessionCurrentlyBeingViewed {
				defer svc.trackCurrentSessionViewers("s1")()
			}
			sess := &Session{ID: "s1", Agent: "claude", Status: tc.status, LastActivityAt: seenAt}
			svc.reportAgentState(t.Context(), []*Session{sess})
			if sess.AgentState != tc.want {
				t.Errorf("AgentState = %q, want %q", sess.AgentState, tc.want)
			}
		})
	}
}

func TestAgentStateReader(t *testing.T) {
	home := t.TempDir()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "deepcove-s1")
	sessionFiles := filepath.Join(home, ".claude", "sessions")
	for _, dir := range []string{stateDir, sessionFiles} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// This test stands in for the pane, and a child it starts for that pane's Claude. A file for any
	// other process does not count.
	claude := exec.Command("cat")
	claudeInput, err := claude.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = claudeInput.Close()
		_ = claude.Wait()
	})
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sessionFiles, strconv.Itoa(claude.Process.Pid)+".json"), `{"sessionId":"s1","status":"idle"}`)
	write(filepath.Join(sessionFiles, "1.json"), `{"sessionId":"s1","status":"busy"}`)
	write(filepath.Join(stateDir, "done"), "")
	write(filepath.Join(stateDir, "escape_sequence"), "state=done:app=codex")
	write(filepath.Join(stateDir, "pid"), strconv.Itoa(os.Getpid()))

	cases := []struct {
		source AgentStateSource
		want   AgentState
	}{
		{source: AgentStateFromClaudeSessionFile, want: AgentStateDone},
		// No codex under the pane, so its last status does not count.
		{source: AgentStateFromEscapeSequence, want: ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.source), func(t *testing.T) {
			cmd := exec.Command("sh", "-c", agentStateReader, "sh", "s1", string(tc.source), "codex")
			cmd.Env = []string{"HOME=" + home, "DEEPCOVE_AGENT_STATE_DIR=" + stateDir, "PATH=" + os.Getenv("PATH")}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("reader: %v", err)
			}
			if got := parseAgentState(string(out), tc.source, time.Now()).state; got != tc.want {
				t.Errorf("state = %q, want %q from:\n%s", got, tc.want, out)
			}
		})
	}
}

func TestSaveEscapeSequenceCommand(t *testing.T) {
	tmp := t.TempDir()
	pane := "\x1b]7501;state=working:app=codex\x1b\\hello\r\n" +
		"\x1b[32mgreen\x1b[0m" +
		"\x1b]7501;state=blocked:app=codex:kind=permission\x07after a bell\r\n"
	cmd := exec.Command("sh", "-c", saveEscapeSequenceCommand("s1"))
	cmd.Env = []string{"DEEPCOVE_AGENT_STATE_DIR=" + filepath.Join(tmp, "state"), "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(pane)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("save: %v: %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "state", "escape_sequence"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "state=blocked:app=codex:kind=permission\n"; string(got) != want {
		t.Errorf("escape_sequence = %q, want %q", got, want)
	}
}

func TestClaudeStateHooks(t *testing.T) {
	cases := []struct {
		event  string
		stdin  string
		before []string
		want   []string
	}{
		{event: "Stop", want: []string{"done"}},
		{event: "StopFailure", stdin: `{"error":"server_error"}`, want: []string{"error"}},
		{event: "UserPromptSubmit", before: []string{"done", "error"}},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			// Quotes in the folder's name, which the hooks have to survive as JSON and as shell.
			stateDir := filepath.Join(t.TempDir(), `it's "quoted"`)
			env := []string{"DEEPCOVE_AGENT_STATE_DIR=" + stateDir, "DEEPCOVE_SESSION_ID=s1", "PATH=" + os.Getenv("PATH")}
			cmd := exec.Command("sh", "-c", agentStateFolder+claudeStateHooks+`cat "$claude_settings"`)
			cmd.Env = env
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("hooks: %v", err)
			}
			var settings struct {
				Hooks map[string][]struct {
					Hooks []struct {
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(out, &settings); err != nil {
				t.Fatalf("settings are not JSON: %v\n%s", err, out)
			}
			for _, name := range tc.before {
				if err := os.WriteFile(filepath.Join(stateDir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hooks := settings.Hooks[tc.event]
			if len(hooks) != 1 || len(hooks[0].Hooks) != 1 {
				t.Fatalf("hooks = %+v, want one command", hooks)
			}
			// The way Claude runs a hook: with the launch script's environment, and the payload on stdin.
			hook := exec.Command("sh", "-c", hooks[0].Hooks[0].Command)
			hook.Env = env
			hook.Stdin = strings.NewReader(tc.stdin)
			if out, err := hook.CombinedOutput(); err != nil {
				t.Fatalf("hook: %v: %s", err, out)
			}
			for _, name := range []string{"done", "error"} {
				_, err := os.Stat(filepath.Join(stateDir, name))
				if got, want := err == nil, slices.Contains(tc.want, name); got != want {
					t.Errorf("%s exists = %t, want %t", name, got, want)
				}
			}
		})
	}
}

func TestReadAgentState_SharesAReadInProgress(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	fake.execResponses = []execResponse{{stdoutAfter: make(chan struct{})}}
	sess := &Session{ID: "s1", Agent: "claude", Runtime: runtimeFake, SandboxID: "sb1"}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			svc.readAgentState(t.Context(), sess)
		})
	}
	wg.Wait()
	if len(fake.execCalls) != 1 {
		t.Errorf("execs = %d, want 1 shared by both reads", len(fake.execCalls))
	}
}

func TestExecCapture(t *testing.T) {
	cases := []struct {
		name     string
		response execResponse
		wantOK   bool
	}{
		{name: "output that fits", response: execResponse{stdout: "now 1000\n"}, wantOK: true},
		{name: "output past the cap", response: execResponse{stdout: strings.Repeat("x", maxCaptureBytes+1)}},
		{name: "a stalled exec, stopped at the deadline", response: execResponse{stdoutAfter: make(chan struct{})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			fake.execResponses = []execResponse{tc.response}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			isCaptured := make(chan bool)
			go func() {
				_, ok := svc.execCapture(ctx, &Session{ID: "s1", Runtime: runtimeFake, SandboxID: "sb1"}, []string{"sh"})
				isCaptured <- ok
			}()
			select {
			case ok := <-isCaptured:
				if ok != tc.wantOK {
					t.Errorf("execCapture() ok = %t, want %t", ok, tc.wantOK)
				}
				if !fake.processes[0].waited {
					t.Error("the process was never waited on, which leaves a zombie behind")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("execCapture() still waiting long after its deadline")
			}
		})
	}
}
