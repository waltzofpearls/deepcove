package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		{name: "done while you are looking", status: StatusRunning, stateRead: agentStateRead{state: AgentStateDone, finishedAt: seenAt.Add(time.Minute)}, isSessionCurrentlyBeingViewed: true, want: AgentStateIdle},
		{name: "a stopped session reads nothing", status: StatusStopped, stateRead: agentStateRead{state: AgentStateWorking}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(nil, nil)
			svc.agentStateCache.bySession = map[string]cachedAgentStateRead{"s1": {readAt: time.Now(), stateRead: tc.stateRead}}
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
	// This test's own pid stands in for a live Claude, and one that cannot exist for a dead one.
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sessionFiles, strconv.Itoa(os.Getpid())+".json"), `{"sessionId":"s1","status":"idle"}`)
	write(filepath.Join(sessionFiles, "999999999.json"), `{"sessionId":"s1","status":"busy"}`)
	write(filepath.Join(sessionFiles, "1.json"), `{"sessionId":"other","status":"busy"}`)
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
			cmd.Env = []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH")}
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
	cmd.Env = []string{"TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(pane)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("save: %v: %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "deepcove-s1", "escape_sequence"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "state=blocked:app=codex:kind=permission\n"; string(got) != want {
		t.Errorf("escape_sequence = %q, want %q", got, want)
	}
}

func TestClaudeStateHooks(t *testing.T) {
	tmp := t.TempDir()
	script := agentStateFolder + claudeStateHooks + `cat "$claude_settings"`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"TMPDIR=" + tmp, "DEEPCOVE_SESSION_ID=s1", "PATH=" + os.Getenv("PATH")}
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
	stateDir := filepath.Join(tmp, "deepcove-s1")
	for _, event := range []string{"UserPromptSubmit", "Stop", "StopFailure"} {
		hooks := settings.Hooks[event]
		if len(hooks) != 1 || len(hooks[0].Hooks) != 1 || !strings.Contains(hooks[0].Hooks[0].Command, stateDir) {
			t.Errorf("%s hook = %+v, want one command in %s", event, hooks, stateDir)
		}
	}
}
