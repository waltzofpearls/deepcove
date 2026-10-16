//go:build !windows

package sandboxless

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// testAgent is a stand in for a registry row, so these tests name no real agent.
func testAgent() sandbox.Agent {
	return sandbox.Agent{
		Name:      "testagent",
		Binary:    "testagent",
		Install:   "installer that the tests replace",
		LaunchCmd: `echo "running $DEEPCOVE_AGENT"`,
	}
}

// fakeAgent puts an executable named after the agent in dir and returns its path.
func fakeAgent(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, testAgent().Binary)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentInstalled(t *testing.T) {
	d := newTestDriver(t)
	if d.AgentInstalled(testAgent()) {
		t.Fatal("nothing has installed the agent yet")
	}

	path := fakeAgent(t, filepath.Join(d.agentHome, ".local", "bin"))
	if !d.AgentInstalled(testAgent()) {
		t.Fatal("the agent is in the agent home now")
	}
	if got := d.agentPaths[testAgent().Binary]; got != path {
		t.Errorf("remembered %q, want %q", got, path)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if d.AgentInstalled(testAgent()) {
		t.Error("the remembered agent is gone, so the answer has to go back to false")
	}
}

// waitForSetup polls until setup settles, the way the session layer does.
func waitForSetup(t *testing.T, d *Driver, instanceID sandbox.ID) sandbox.SetupState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := d.Setup(context.Background(), instanceID)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if state.Status != sandbox.SetupInstalling {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("setup never finished")
	return sandbox.SetupState{}
}

func TestStart_TwoSessionsShareOneInstall(t *testing.T) {
	d := newTestDriver(t)
	var (
		mu       sync.Mutex
		installs int
		atOnce   int
		worst    int
	)
	d.installAgent = func(_ context.Context, agentHome, _ string) (string, error) {
		mu.Lock()
		installs++
		atOnce++
		worst = max(worst, atOnce)
		mu.Unlock()

		time.Sleep(50 * time.Millisecond)
		fakeAgent(t, filepath.Join(agentHome, ".local", "bin"))

		mu.Lock()
		atOnce--
		mu.Unlock()
		return "test installer", nil
	}

	first, second := startedInstance(t, d), startedInstance(t, d)
	for _, instanceID := range []sandbox.ID{first, second} {
		if state := waitForSetup(t, d, instanceID); state.Status != sandbox.SetupReady {
			t.Fatalf("%s: setup %s: %s", instanceID, state.Status, state.Log)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if worst > 1 {
		t.Errorf("%d installers ran at once, writing the same files", worst)
	}
	if installs != 1 {
		t.Errorf("ran the installer %d times, want the second session to find it already here", installs)
	}
}

// startedInstance is created(t, d) without its wait, so two of them can install at the same time.
func startedInstance(t *testing.T, d *Driver) sandbox.ID {
	t.Helper()
	instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{Cmd: []string{"/bin/sh"}, Agent: testAgent()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return instanceID
}

func TestStop_CancelsAnInstallInFlight(t *testing.T) {
	d := newTestDriver(t)
	installing := make(chan struct{})
	stopped := make(chan error, 1)
	d.installAgent = func(ctx context.Context, _, _ string) (string, error) {
		close(installing)
		<-ctx.Done()
		stopped <- ctx.Err()
		return "", ctx.Err()
	}
	instanceID := startedInstance(t, d)
	<-installing

	if err := d.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the install ended with %v, want a cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the install is still running after Stop")
	}
}

func TestStart(t *testing.T) {
	cases := []struct {
		name string
		// gone deletes the agent between the stop and the start.
		gone         bool
		wantInstalls int32
	}{
		{name: "the agent is still here", wantInstalls: 1},
		{name: "the agent went missing", gone: true, wantInstalls: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			var installs atomic.Int32
			d.installAgent = func(_ context.Context, agentHome, _ string) (string, error) {
				installs.Add(1)
				fakeAgent(t, filepath.Join(agentHome, ".local", "bin"))
				return "test installer", nil
			}
			instanceID := created(t, d)
			inst, err := d.lookup(instanceID)
			if err != nil {
				t.Fatal(err)
			}

			if err := d.Stop(context.Background(), instanceID); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if tc.gone {
				if err := os.Remove(inst.agentPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Start(context.Background(), instanceID); err != nil {
				t.Fatalf("Start: %v", err)
			}

			if state := waitForSetup(t, d, instanceID); state.Status != sandbox.SetupReady {
				t.Fatalf("setup says %q: %s", state.Status, state.Log)
			}
			if got := installs.Load(); got != tc.wantInstalls {
				t.Errorf("the installer ran %d times, want %d", got, tc.wantInstalls)
			}
		})
	}
}

func TestStart_AfterAStopMidInstall(t *testing.T) {
	d := newTestDriver(t)
	installing := make(chan struct{})
	var tries atomic.Int32
	d.installAgent = func(ctx context.Context, agentHome, _ string) (string, error) {
		if tries.Add(1) == 1 {
			close(installing)
			<-ctx.Done()
			return "", ctx.Err()
		}
		fakeAgent(t, filepath.Join(agentHome, ".local", "bin"))
		return "test installer", nil
	}
	instanceID := startedInstance(t, d)
	<-installing

	if err := d.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := d.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if state := waitForSetup(t, d, instanceID); state.Status != sandbox.SetupReady {
		t.Fatalf("setup says %q: %s", state.Status, state.Log)
	}
	if got := tries.Load(); got != 2 {
		t.Errorf("the installer ran %d times, want a second try after the stop", got)
	}
}

func TestStart_AlreadyRunning(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)

	// The VM runtime says the same, and the session layer never asks twice.
	if err := d.Start(context.Background(), instanceID); !errors.Is(err, sandbox.ErrConflict) {
		t.Errorf("Start: %v, want ErrConflict", err)
	}
}

func TestClose(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)
	inst := instanceOf(t, d, instanceID)
	deadSocket(t, inst.socket)
	d.killServer = func(context.Context, string) error {
		t.Error("stopped an agent that deepcove does not own")
		return nil
	}

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The agent outlives deepcove, and the next start takes it back.
	if _, err := os.Stat(inst.socket); err != nil {
		t.Errorf("the socket was removed: %v", err)
	}
}

func TestClose_CancelsAnInstallInFlight(t *testing.T) {
	d := newTestDriver(t)
	installing := make(chan struct{})
	stopped := make(chan error, 1)
	d.installAgent = func(ctx context.Context, _, _ string) (string, error) {
		close(installing)
		<-ctx.Done()
		stopped <- ctx.Err()
		return "", ctx.Err()
	}
	startedInstance(t, d)
	<-installing

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the install ended with %v, want a cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the install is still running after Close")
	}
}

func TestRunInstall(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "written-after-the-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runInstall(ctx, dir, "sh -c 'sleep 1; touch "+marker+"' | cat")
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Past when the child would have written it, had it lived.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a child of the install kept running after the cancel")
	}
}

func TestRunInstall_HasNoTerminal(t *testing.T) {
	// An installer that finds a terminal can stop and wait for an answer nobody will type, which is how
	// Codex's installer hung asking "Start Codex now?" on the terminal deepcove was started from. So the
	// install runs in a session of its own, which has no terminal.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runInstall(context.Background(), dir, "echo $$ > "+pidFile+"; sleep 2")
	}()
	defer func() { <-done }()

	var pid int
	for deadline := time.Now().Add(time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if pid == 0 {
		t.Fatal("the install never started")
	}
	session, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if session != pid {
		t.Errorf("the install runs in session %d, want a session of its own (%d)", session, pid)
	}
}

func TestKillGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatalf("the test command: %v", err)
	}

	if err := killGroup(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("killGroup said %v, want os.ErrProcessDone so os/exec leaves the result alone", err)
	}
}

func TestProvisionAgent(t *testing.T) {
	cases := []struct {
		name string
		// ctx is the setup context in the state the case is about.
		ctx        func(t *testing.T) context.Context
		wantStatus sandbox.SetupStatus
	}{
		{
			name: "a stop cancelled it",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "it ran out of time",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			wantStatus: sandbox.SetupFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			d.installAgent = func(ctx context.Context, _, _ string) (string, error) {
				return "the log so far", ctx.Err()
			}
			instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
				Cmd:   []string{"/bin/sh"},
				Agent: testAgent(),
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			d.provisionAgent(tc.ctx(t), instanceID, instanceOf(t, d, instanceID))

			// Anything but failed leaves the session saying it is installing, which no start retries.
			state, err := d.Setup(context.Background(), instanceID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != tc.wantStatus {
				t.Errorf("setup says %q, want %q", state.Status, tc.wantStatus)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	cases := []struct {
		name string
		// agent is the registry row this session is created with.
		agent sandbox.Agent
		// place puts an agent somewhere before the session starts, and says where the script should point.
		place      func(t *testing.T, d *Driver) string
		install    func(ctx context.Context, agentHome, install string) (string, error)
		wantStatus sandbox.SetupStatus
		wantLog    string
	}{
		{
			name:  "the agent is already on this machine",
			agent: testAgent(),
			place: func(t *testing.T, d *Driver) string {
				dir := t.TempDir()
				path := fakeAgent(t, dir)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				return path
			},
			install:    refuseToInstall(t),
			wantStatus: sandbox.SetupReady,
		},
		{
			name:  "an earlier session installed it here",
			agent: testAgent(),
			place: func(t *testing.T, d *Driver) string {
				return fakeAgent(t, filepath.Join(d.agentHome, ".local", "bin"))
			},
			install:    refuseToInstall(t),
			wantStatus: sandbox.SetupReady,
		},
		{
			name:  "this session installs it",
			agent: testAgent(),
			place: func(t *testing.T, d *Driver) string {
				return filepath.Join(d.agentHome, ".local", "bin", testAgent().Binary)
			},
			wantStatus: sandbox.SetupReady,
		},
		{
			name:  "the installer failed",
			agent: testAgent(),
			install: func(context.Context, string, string) (string, error) {
				return "curl: (6) could not resolve host", errors.New("exit status 6")
			},
			wantStatus: sandbox.SetupFailed,
			wantLog:    "could not resolve host",
		},
		{
			name:  "the installer left nothing behind",
			agent: testAgent(),
			install: func(context.Context, string, string) (string, error) {
				return "all done", nil
			},
			wantStatus: sandbox.SetupFailed,
			wantLog:    "left no testagent",
		},
		{
			name:       "the agent cannot be installed at all",
			agent:      sandbox.Agent{Name: "testagent", Binary: "testagent"},
			wantStatus: sandbox.SetupFailed,
			wantLog:    "no install command",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			var want string
			if tc.place != nil {
				want = tc.place(t, d)
			}
			if tc.install != nil {
				d.installAgent = tc.install
			}
			instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
				Cmd:   []string{"/bin/sh"},
				Agent: tc.agent,
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := d.Start(context.Background(), instanceID); err != nil {
				t.Fatalf("Start: %v", err)
			}

			state := waitForSetup(t, d, instanceID)
			if state.Status != tc.wantStatus {
				t.Fatalf("status %q, want %q, log: %s", state.Status, tc.wantStatus, state.Log)
			}
			if tc.wantLog != "" && !strings.Contains(state.Log, tc.wantLog) {
				t.Errorf("log %q, want it to mention %q", state.Log, tc.wantLog)
			}
			// The script has to name the agent that was found, since it runs that path and nothing else.
			if want != "" && !strings.Contains(launchScriptBody(t, d, instanceID), shellQuote(want)) {
				t.Errorf("the launch script does not point at %s", want)
			}
		})
	}
}

func refuseToInstall(t *testing.T) func(context.Context, string, string) (string, error) {
	return func(context.Context, string, string) (string, error) {
		t.Error("installed an agent that was already there")
		return "", errors.New("should not install")
	}
}

func TestResolveAgent(t *testing.T) {
	d := newTestDriver(t)
	type call struct{ home, install string }
	got := make(chan call, 1)
	d.installAgent = func(_ context.Context, agentHome, install string) (string, error) {
		got <- call{agentHome, install}
		fakeAgent(t, filepath.Join(agentHome, ".local", "bin"))
		return "", nil
	}
	created(t, d)

	c := <-got
	if c.home != d.agentHome {
		t.Errorf("installed into %q, want %q", c.home, d.agentHome)
	}
	if c.install != testAgent().Install {
		t.Errorf("ran %q, want the agent's own install command", c.install)
	}
}

func TestWriteLaunchScript(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)

	body := launchScriptBody(t, d, instanceID)
	want := "DEEPCOVE_AGENT=" + shellQuote(filepath.Join(d.agentHome, ".local", "bin", testAgent().Binary))
	if !strings.Contains(body, want) {
		t.Errorf("want %q in:\n%s", want, body)
	}
	if !strings.Contains(body, testAgent().LaunchCmd) {
		t.Errorf("the agent's launch command is missing from:\n%s", body)
	}
}

func TestWriteLaunchScript_RunsTheAgent(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)
	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}

	launch, err := d.AgentLaunchCommand(instanceID, "claude")
	if err != nil {
		t.Fatalf("AgentLaunchCommand: %v", err)
	}
	p, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{
		Cmd:        launch,
		WorkingDir: inst.dir,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	out := readAll(t, p.Stdout())
	if _, err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	want := "running " + filepath.Join(d.agentHome, ".local", "bin", testAgent().Binary)
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestAgentLaunchCommand(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)
	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := d.AgentLaunchCommand(instanceID, "claude")
	if err != nil {
		t.Fatalf("AgentLaunchCommand: %v", err)
	}
	want := []string{"sh", filepath.Join(inst.dir, "agent-launch.sh")}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
	// An instance nobody knows has no command, and saying so beats handing back an empty one that tmux
	// would try to run.
	if _, err := d.AgentLaunchCommand("sandboxless-nope", "claude"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestWorkDir(t *testing.T) {
	d := newTestDriver(t)
	instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
		SessionID: "s-42",
		Cmd:       []string{"/bin/sh"},
		Agent:     testAgent(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := d.WorkDir("s-42", nil)
	if want := filepath.Join(d.stateDir, "workspaces", "s-42"); work != want {
		t.Fatalf("got %q, want %q", work, want)
	}

	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(work, inst.dir) {
		t.Errorf("%q is inside the instance folder %q", work, inst.dir)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{name: "a plain path", in: "/home/deep/.local/bin/claude", want: `'/home/deep/.local/bin/claude'`},
		{name: "an apostrophe", in: "/Users/o'brien/.local/bin/claude", want: `'/Users/o'\''brien/.local/bin/claude'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellQuote(tc.in); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func launchScriptBody(t *testing.T, d *Driver, instanceID sandbox.ID) string {
	t.Helper()
	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(launchScript(inst.dir))
	if err != nil {
		t.Fatalf("launch script: %v", err)
	}
	return string(body)
}

func TestDestroy_CancelsAnInstallInFlight(t *testing.T) {
	d := newTestDriver(t)
	installing := make(chan struct{})
	stopped := make(chan error, 1)
	d.installAgent = func(ctx context.Context, _, _ string) (string, error) {
		close(installing)
		<-ctx.Done()
		stopped <- ctx.Err()
		return "", ctx.Err()
	}

	instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Cmd:   []string{"/bin/sh"},
		Agent: testAgent(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-installing

	if err := d.Destroy(context.Background(), instanceID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the install ended with %v, want it cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the install is still running after the session went")
	}
	if _, err := os.Stat(inst.dir); !os.IsNotExist(err) {
		t.Errorf("the instance folder is still there: %v", err)
	}
}

func TestResolveAgent_MakesTheAgentHome(t *testing.T) {
	d := newTestDriver(t)
	if err := os.RemoveAll(d.agentHome); err != nil {
		t.Fatal(err)
	}

	instanceID := created(t, d)
	if state := waitForSetup(t, d, instanceID); state.Status != sandbox.SetupReady {
		t.Fatalf("status %q, log: %s", state.Status, state.Log)
	}
	if _, err := os.Stat(d.agentHome); err != nil {
		t.Errorf("the agent home is not there after an install: %v", err)
	}
}
