//go:build !windows

package sandboxless

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

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

func TestSweepOrphans(t *testing.T) {
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

			swept, err := d.SweepOrphans(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("SweepOrphans: %v, wantErr %v", err, tc.wantErr)
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

func TestSweepOrphans_AdoptsAServerItCannotStop(t *testing.T) {
	d := newTestDriver(t)
	d.killServer = func(context.Context, string) error { return errors.New("still running") }
	instanceID := sandbox.ID(idPrefix + "BBBBBBBBZZZZ")
	if err := os.MkdirAll(filepath.Join(d.instancesDir(), string(instanceID)), 0o700); err != nil {
		t.Fatal(err)
	}
	deadSocket(t, filepath.Join(d.socketDir, "BBBBBBBB.sock"))

	if _, err := d.SweepOrphans(context.Background()); err == nil {
		t.Fatal("the sweep reported success for a server it could not stop")
	}

	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatalf("the session was not taken back: %v", err)
	}
	if !inst.running {
		t.Error("taken back as stopped, so nothing will stop its agent")
	}
	// The session layer marks a row lost when its sandbox is not in this list, then rebuilds it, which
	// is how a second agent ends up in the folder the first is working in.
	listed, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != instanceID {
		t.Errorf("listed %v, want the session that is still running", listed)
	}
}

func TestSweepOrphans_AnAdoptedSessionStartsAgain(t *testing.T) {
	first := newTestDriver(t)
	instanceID := created(t, first)
	inst := instanceOf(t, first, instanceID)
	deadSocket(t, inst.socket)

	// deepcove restarts: a second driver over the same folders, and a server that will not die.
	second, err := NewDriver(first.stateDir, first.agentHome, filepath.Dir(first.socketDir))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	second.killServer = func(context.Context, string) error { return errors.New("still running") }
	second.installAgent = func(context.Context, string, string) (string, error) {
		t.Error("installed an agent again for a session that already has one")
		return "", nil
	}
	if _, err := second.SweepOrphans(context.Background()); err == nil {
		t.Fatal("the sweep reported success for a server it could not stop")
	}

	second.killServer = func(context.Context, string) error { return nil }
	if err := second.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := second.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Without the agent it runs, the start fails with "the spec names no agent binary" and the session
	// can never come back.
	if state := waitForSetup(t, second, instanceID); state.Status != sandbox.SetupReady {
		t.Fatalf("setup says %q: %s", state.Status, state.Log)
	}
}
