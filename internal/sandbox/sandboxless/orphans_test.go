//go:build !windows

package sandboxless

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

var errNoAnswer = errors.New("tmux could not be run")

// deadSocket leaves a socket file with nobody behind it, which is what a killed tmux leaves.
func deadSocket(t *testing.T, path string) string {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSweep(t *testing.T) {
	cases := []struct {
		name string
		// arrange sets the driver up, and says what must be gone afterwards and what must survive.
		arrange   func(t *testing.T, d *Driver) (gone, kept []string)
		wantSwept int
		wantErr   bool
	}{
		{
			name: "a socket nobody owns",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				mine := instanceOf(t, d, created(t, d))
				notOurs := filepath.Join(d.socketDir, "notes.txt")
				if err := os.WriteFile(notOurs, []byte("keep me"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{deadSocket(t, filepath.Join(d.socketDir, "AAAAAAAA.sock"))},
					[]string{deadSocket(t, mine.socket), notOurs}
			},
			wantSwept: 1,
		},
		{
			name: "a socket in the folder the VM runtime shares with us",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				return nil, []string{deadSocket(t, filepath.Join(filepath.Dir(d.socketDir), "api.sock"))}
			},
		},
		{
			name: "no socket folder at all",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				d.socketDir = filepath.Join(t.TempDir(), "not-there")
				return nil, nil
			},
		},
		{
			name: "a server that will not die",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				d.killServer = func(context.Context, string) error { return errors.New("still running") }
				// The folder that server works in, named after the id its socket is short for.
				dir := filepath.Join(d.instancesDir(), idPrefix+"BBBBBBBBZZZZ")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				return nil, []string{deadSocket(t, filepath.Join(d.socketDir, "BBBBBBBB.sock")), dir}
			},
			wantErr: true,
		},
		{
			name: "no socket folder to read",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				dir := filepath.Join(d.instancesDir(), idPrefix+"CCCCCCCCZZZZ")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				// A file where the folder should be, so the scan fails and nothing is known to be gone.
				blocked := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				d.socketDir = blocked
				return nil, []string{dir}
			},
			wantErr: true,
		},
		{
			name: "the folder a previous run's session worked from",
			arrange: func(t *testing.T, d *Driver) ([]string, []string) {
				mine := instanceOf(t, d, created(t, d))
				old := filepath.Join(d.instancesDir(), "sandboxless-FROMALASTRUN")
				if err := os.MkdirAll(old, 0o700); err != nil {
					t.Fatal(err)
				}
				return []string{old}, []string{mine.dir}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			gone, kept := tc.arrange(t, d)

			swept, err := d.sweep(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("sweep: %v, wantErr %v", err, tc.wantErr)
			}
			if swept != tc.wantSwept {
				t.Errorf("swept %d, want %d", swept, tc.wantSwept)
			}
			for _, path := range gone {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("%s is still there: %v", path, err)
				}
			}
			for _, path := range kept {
				// A socket removed while its server lives leaves an agent nothing can reach.
				if _, err := os.Stat(path); err != nil {
					t.Errorf("%s was removed: %v", path, err)
				}
			}
		})
	}
}

func TestKillTmuxServer(t *testing.T) {
	cases := []struct {
		name string
		// done cancels the context before the call, the way a browser leaving mid delete does.
		done    bool
		wantErr error
	}{
		{name: "a socket with no server behind it"},
		{name: "a context that ended first", done: true, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.done {
				cancel()
			}

			// Reading a command that died with the context as "the server is gone" removes the socket
			// of one that is still running.
			err := killTmuxServer(ctx, filepath.Join(t.TempDir(), "nothing.sock"))
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestAdopt(t *testing.T) {
	cases := []struct {
		name string
		// id is for a case that needs one of its own. The rest share a plain id.
		id sandbox.ID
		// left puts what a previous run would have left on disk for this session.
		left  func(t *testing.T, d *Driver, instanceID sandbox.ID)
		alive bool
		// probeErr is tmux failing to answer, which is not an answer of "no".
		probeErr    error
		wantAdopted bool
		wantErr     bool
	}{
		{name: "its server is still running", left: leaveASession, alive: true, wantAdopted: true},
		{name: "its server is gone", left: leaveASession},
		{name: "its folder is gone", left: func(*testing.T, *Driver, sandbox.ID) {}, alive: true},
		{name: "its server cannot be asked about", left: leaveASession, probeErr: errNoAnswer, wantAdopted: true, wantErr: true},
		{name: "its folder cannot be looked at", left: leaveUnreadableSession, alive: true, wantAdopted: true, wantErr: true},
		{
			name: "an id that could climb out of the instances folder",
			id:   idPrefix + "../../../elsewhere",
			left: func(t *testing.T, d *Driver, _ sandbox.ID) {
				// Where that id lands once its dots are resolved, which is outside the instances folder.
				if err := os.MkdirAll(filepath.Join(d.stateDir, "elsewhere"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			alive: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			instanceID := cmp.Or(tc.id, sandbox.ID(idPrefix+"ADOPTED1ZZZZ"))
			tc.left(t, d, instanceID)
			d.serverAlive = func(context.Context, string) (bool, error) { return tc.alive, tc.probeErr }
			d.killServer = func(context.Context, string) error { return nil }

			sandboxes := map[sandbox.ID]sandbox.SandboxSpec{instanceID: {Agent: testAgent()}}
			taken, err := d.Adopt(context.Background(), sandboxes)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("Adopt: %v, want an error: %v", err, tc.wantErr)
			}
			if wantTaken := len(taken) == 1; wantTaken != tc.wantAdopted {
				t.Errorf("took back %v, want %v", taken, tc.wantAdopted)
			}
			// What it could not take back is simply not in its list, which is how the session layer
			// learns to rebuild that session.
			_, lookupErr := d.lookup(instanceID)
			if adopted := lookupErr == nil; adopted != tc.wantAdopted {
				t.Errorf("adopted %v, want %v", adopted, tc.wantAdopted)
			}
		})
	}
}

func TestAdopt_TakesBackTheAgentItWasRunning(t *testing.T) {
	first := newTestDriver(t)
	instanceID := created(t, first)
	inst := instanceOf(t, first, instanceID)
	deadSocket(t, inst.socket)

	second, err := NewDriver(first.stateDir, first.agentHome, filepath.Dir(first.socketDir))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	second.serverAlive = func(context.Context, string) (bool, error) { return true, nil }
	second.killServer = func(context.Context, string) error { return nil }
	second.installAgent = func(context.Context, string, string) (string, error) {
		t.Error("installed an agent again for a session that already has one")
		return "", nil
	}

	sandboxes := map[sandbox.ID]sandbox.SandboxSpec{instanceID: {Agent: testAgent()}}
	if taken, err := second.Adopt(context.Background(), sandboxes); err != nil || len(taken) != 1 {
		t.Fatalf("Adopt: took back %v, err %v", taken, err)
	}
	if err := second.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := second.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Without the agent from its record the start fails with "the spec names no agent binary".
	if state := waitForSetup(t, second, instanceID); state.Status != sandbox.SetupReady {
		t.Fatalf("setup says %q: %s", state.Status, state.Log)
	}
}

func TestAdopt_ItsAgentMovedSinceItStarted(t *testing.T) {
	first := newTestDriver(t)
	instanceID := created(t, first)
	inst := instanceOf(t, first, instanceID)
	deadSocket(t, inst.socket)

	// The agent its script names is gone, and there is one somewhere else now.
	if err := os.Remove(agentInLaunchScript(inst.dir)); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	fakeAgent(t, elsewhere)
	t.Setenv("PATH", elsewhere+string(os.PathListSeparator)+os.Getenv("PATH"))

	second, err := NewDriver(first.stateDir, first.agentHome, filepath.Dir(first.socketDir))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	second.serverAlive = func(context.Context, string) (bool, error) { return true, nil }
	second.killServer = func(context.Context, string) error { return nil }

	sandboxes := map[sandbox.ID]sandbox.SandboxSpec{instanceID: {Agent: testAgent()}}
	if taken, err := second.Adopt(context.Background(), sandboxes); err != nil || len(taken) != 1 {
		t.Fatalf("Adopt: took back %v, err %v", taken, err)
	}
	if err := second.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := second.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state := waitForSetup(t, second, instanceID); state.Status != sandbox.SetupReady {
		t.Fatalf("setup says %q: %s", state.Status, state.Log)
	}
	// Started without this, the pane runs a path that is not there and dies with nothing in it.
	if path := agentInLaunchScript(instanceOf(t, second, instanceID).dir); !isExecutable(path) {
		t.Errorf("its launch script still runs %q, which is not there", path)
	}
}

func TestAdopt_ReportsWhenItWasCreated(t *testing.T) {
	d := newTestDriver(t)
	instanceID := sandbox.ID(idPrefix + "ADOPTED1ZZZZ")
	leaveASession(t, d, instanceID)
	d.serverAlive = func(context.Context, string) (bool, error) { return true, nil }
	d.killServer = func(context.Context, string) error { return nil }

	made := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(d.instancesDir(), string(instanceID)), made, made); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Adopt(context.Background(), map[sandbox.ID]sandbox.SandboxSpec{instanceID: {}}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	// Otherwise every session on the node page reads as made at the last restart.
	if got := instanceOf(t, d, instanceID).createdAt; !got.Equal(made) {
		t.Errorf("created at %v, want %v", got, made)
	}
}

func TestTmuxServerAlive(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	// No server on this socket, which tmux can say for certain.
	alive, err := tmuxServerAlive(context.Background(), filepath.Join(t.TempDir(), "nothing.sock"))
	if alive || err != nil {
		t.Errorf("got alive %v, err %v, want a plain no", alive, err)
	}
}

func TestAdopt_ItsServerNeverAnswers(t *testing.T) {
	d := newTestDriver(t)
	instanceID := sandbox.ID(idPrefix + "ADOPTED1ZZZZ")
	leaveASession(t, d, instanceID)
	// What a wedged tmux does: it takes the call and never comes back. Only the context ends it.
	d.serverAlive = func(ctx context.Context, _ string) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	d.killServer = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	d.tmuxCallBudget = 20 * time.Millisecond

	done := make(chan []sandbox.ID, 1)
	go func() {
		taken, _ := d.Adopt(context.Background(), map[sandbox.ID]sandbox.SandboxSpec{instanceID: {}})
		done <- taken
	}()
	select {
	case taken := <-done:
		// Kept, not swept: nothing said its agent was gone.
		if len(taken) != 1 {
			t.Errorf("took back %v, want the one it could not ask about", taken)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("adoption never finished, so the node would never start")
	}
}

func TestAdopt_ManyServersNeverAnswer(t *testing.T) {
	d := newTestDriver(t)
	d.serverAlive = func(ctx context.Context, _ string) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	d.killServer = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	d.tmuxCallBudget = 50 * time.Millisecond
	d.adoptBudget = 100 * time.Millisecond

	sandboxes := map[sandbox.ID]sandbox.SandboxSpec{}
	for i := range 40 {
		instanceID := sandbox.ID(fmt.Sprintf("%sWEDGED%02d", idPrefix, i))
		leaveASession(t, d, instanceID)
		sandboxes[instanceID] = sandbox.SandboxSpec{}
	}

	done := make(chan []sandbox.ID, 1)
	go func() {
		taken, _ := d.Adopt(context.Background(), sandboxes)
		done <- taken
	}()
	select {
	case taken := <-done:
		// All of them: nothing said any of their agents was gone.
		if len(taken) != len(sandboxes) {
			t.Errorf("took back %d, want %d", len(taken), len(sandboxes))
		}
	case <-time.After(time.Second):
		t.Fatal("adoption outlasted its own budget, so the node would be down for minutes")
	}
}

// leaveUnreadableSession leaves a session whose folder cannot be looked at, by taking the permissions
// off the folder holding it.
func leaveUnreadableSession(t *testing.T, d *Driver, instanceID sandbox.ID) {
	t.Helper()
	leaveASession(t, d, instanceID)
	if err := os.Chmod(d.instancesDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.instancesDir(), 0o700) })
	if _, err := os.Stat(filepath.Join(d.instancesDir(), string(instanceID))); err == nil {
		t.Skip("this user can look inside a folder with no permissions")
	}
}

// leaveASession writes the folder, launch script and socket a previous run would have left behind.
func leaveASession(t *testing.T, d *Driver, instanceID sandbox.ID) {
	t.Helper()
	dir := filepath.Join(d.instancesDir(), string(instanceID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := fakeAgent(t, filepath.Join(d.agentHome, ".local", "bin"))
	if err := writeLaunchScript(dir, agent, testAgent().LaunchCmd); err != nil {
		t.Fatal(err)
	}
	deadSocket(t, filepath.Join(d.socketDir, shortID(instanceID)+".sock"))
}
