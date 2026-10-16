package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestTmuxSessionName(t *testing.T) {
	// One tmux session per deepcove session, so two sessions on one machine cannot collide.
	a := tmuxSessionName("019fddb0-b8c0-727c-87aa-36c21b67b447")
	b := tmuxSessionName("019fddb0-b8c0-727c-87aa-000000000000")
	if a == b {
		t.Fatal("two sessions produced the same tmux session name")
	}
	if !strings.Contains(a, "019fddb0-b8c0-727c-87aa-36c21b67b447") {
		t.Errorf("got %q, want it to carry the session id", a)
	}
	// tmux treats a colon as a window separator and a dot as a pane separator.
	if strings.ContainsAny(a, ":.") {
		t.Errorf("%q contains a character tmux uses as a target separator", a)
	}
}

func TestStartAgentSession(t *testing.T) {
	t.Run("starts the agent in its own tmux session and workdir", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		sess := mustRunning(t, svc)
		// has-session fails (nothing running yet), then mkdir, new-session and set-option succeed.
		fake.execResponses = []execResponse{{exit: 1}, {exit: 0}, {exit: 0}, {exit: 0}}
		fake.execCalls = nil

		if err := svc.startAgentSession(context.Background(), sess, "/workspace/repo"); err != nil {
			t.Fatalf("startAgentSession: %v", err)
		}

		if len(fake.execCalls) != 4 {
			t.Fatalf("expected has-session, mkdir, new-session, set-option; got %d: %v",
				len(fake.execCalls), fake.execCalls)
		}
		if got := strings.Join(fake.execCalls[1], " "); got != "mkdir -p /workspace/repo" {
			t.Errorf("second call %q, want the work dir created", got)
		}
		create := strings.Join(fake.execCalls[2], " ")
		for _, want := range []string{
			"new-session", "-d",
			"-s " + tmuxSessionName(sess.ID),
			"-n " + AgentTmuxWindowName,
			"-c /workspace/repo",
			// The launch script pins the conversation to this, so two sessions in one directory on a
			// shared machine do not resume each other.
			"DEEPCOVE_SESSION_ID=" + sess.ID + " " + sandbox.SandboxedAgentLaunchPath("claude"),
			// Whatever the machine holds, this session's answer is the only one that counts.
			"env -u ANTHROPIC_API_KEY -u ANTHROPIC_AUTH_TOKEN -u ANTHROPIC_BASE_URL",
		} {
			if !strings.Contains(create, want) {
				t.Errorf("new-session missing %q, got: %s", want, create)
			}
		}
	})

	// A sandboxless session keeps its tmux session across a deepcove restart, so starting again must not
	// spawn a second agent.
	t.Run("leaves an existing tmux session alone", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		sess := mustRunning(t, svc)
		fake.execResponses = []execResponse{{exit: 0}}
		fake.execCalls = nil

		if err := svc.startAgentSession(context.Background(), sess, "/workspace"); err != nil {
			t.Fatalf("startAgentSession: %v", err)
		}
		if len(fake.execCalls) != 1 {
			t.Errorf("expected only has-session, got %d: %v", len(fake.execCalls), fake.execCalls)
		}
	})

	t.Run("a failure to start is reported", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		sess := mustRunning(t, svc)
		fake.execResponses = []execResponse{{exit: 1}, {exit: 1}}
		fake.execCalls = nil

		if err := svc.startAgentSession(context.Background(), sess, "/workspace"); err == nil {
			t.Fatal("expected an error when new-session fails")
		}
	})
}

// The entrypoint prepares the machine and stops there. Agents are started by deepcove, because a
// machine can carry several sessions and a reboot must bring all of them back, not just one.
func TestEntrypointPreparesButDoesNotLaunch(t *testing.T) {
	body := strings.Join(buildEntrypoint("NAME", "SETUP", map[string]string{"claude": "LAUNCH"}), " ")
	for _, want := range []string{`session "NAME"`, "SETUP", "#!/bin/sh\nLAUNCH"} {
		if !strings.Contains(body, want) {
			t.Errorf("entrypoint missing %q", want)
		}
	}
	if strings.Contains(body, "new-session") {
		t.Error("the entrypoint starts an agent, which leaves a rebooted machine's other sessions dead")
	}
}

// The entrypoint writes these paths and words as shell literals, and the runtime reads them back
// through the constants. This is what keeps the two sides saying the same thing.
func TestEntrypointWritesWhatTheRuntimeReadsBack(t *testing.T) {
	body := strings.Join(buildEntrypoint("NAME", "SETUP", map[string]string{"claude": "LAUNCH"}), " ")
	want := []string{
		"echo " + sandbox.SandboxedStatusInstalling + " > " + sandbox.SandboxedSetupStatusPath,
		"echo " + sandbox.SandboxedStatusErrored + " > " + sandbox.SandboxedSetupStatusPath,
		"echo " + sandbox.SandboxedStatusReady + " > " + sandbox.SandboxedSetupStatusPath,
		"cat > " + sandbox.SandboxedAgentLaunchPath("claude"),
		sandbox.SandboxedSetupLogPath,
	}
	for _, line := range want {
		if !strings.Contains(body, line) {
			t.Errorf("the entrypoint never does %q", line)
		}
	}
}

func TestBuildEntrypoint_RecordsHowSetupEnded(t *testing.T) {
	cases := []struct {
		name        string
		setup       string
		wantStatus  string
		wantFailure bool
	}{
		{name: "a setup that works", setup: "true", wantStatus: sandbox.SandboxedStatusReady},
		{name: "a setup that fails", setup: "false", wantStatus: sandbox.SandboxedStatusErrored, wantFailure: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The script writes to absolute guest paths, so run it with each one moved under a temp dir. A new
			// path in buildEntrypoint needs a line here too, or this test writes to the real one.
			root := t.TempDir()
			moved := strings.NewReplacer("/etc", root+"/etc", "/var/log", root+"/var/log",
				"/usr/local/bin", root+"/usr/local/bin", "/tmp/", root+"/tmp/", "/workspace", root+"/workspace")
			argv := buildEntrypoint("NAME", tc.setup, map[string]string{"claude": "LAUNCH"})
			err := exec.Command("sh", "-c", moved.Replace(argv[len(argv)-1])).Run()
			if failed := err != nil; failed != tc.wantFailure {
				t.Errorf("got exit error %v, want failure %v", err, tc.wantFailure)
			}
			status, readErr := os.ReadFile(root + sandbox.SandboxedSetupStatusPath)
			if readErr != nil {
				t.Fatalf("read status: %v", readErr)
			}
			if got := strings.TrimSpace(string(status)); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}
