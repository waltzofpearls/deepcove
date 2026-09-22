//go:build !windows

package sandboxless

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// sockets stands in for the runtime directory, and short is the whole point: a unix socket path that
// grows past ~108 bytes cannot be opened at all.
func sockets(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func instanceOf(t *testing.T, d *Driver, instanceID sandbox.ID) *instance {
	t.Helper()
	inst, err := d.lookup(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	dir := t.TempDir()
	d, err := NewDriver(filepath.Join(dir, "sandboxless"), filepath.Join(dir, "agent-home"), sockets(t))
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	// No test may reach the network, and a machine without the agent would otherwise run the real
	// installer. This one puts a stub where the installer would have.
	d.installAgent = func(_ context.Context, agentHome, _ string) (string, error) {
		fakeAgent(t, filepath.Join(agentHome, ".local", "bin"))
		return "test installer", nil
	}
	return d
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func created(t *testing.T, d *Driver) sandbox.ID {
	t.Helper()
	instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Cmd:   []string{"/bin/sh"},
		Agent: testAgent(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Start(context.Background(), instanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start provisions the agent in the background. Waiting here keeps a test from ending, and its
	// folders from being removed, while that is still running.
	waitForSetup(t, d, instanceID)
	return instanceID
}

func TestNewDriver(t *testing.T) {
	cases := []struct {
		name string
		// agentHome hands the driver a path, and puts it in the state the case is about.
		agentHome func(t *testing.T, dir string) string
		wantErr   bool
	}{
		{
			name:      "an agent home that is not there yet",
			agentHome: func(_ *testing.T, dir string) string { return filepath.Join(dir, "agent-home") },
		},
		{
			name: "an agent home it cannot write",
			agentHome: func(t *testing.T, dir string) string {
				locked := filepath.Join(dir, "agent-home")
				if err := os.Mkdir(locked, 0o500); err != nil {
					t.Fatal(err)
				}
				return locked
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr && os.Getuid() == 0 {
				t.Skip("root writes anywhere")
			}
			dir := t.TempDir()
			home := tc.agentHome(t, dir)

			_, err := NewDriver(filepath.Join(dir, "sandboxless"), home, sockets(t))
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewDriver: %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			// A node that never runs one of these sessions gets nothing written for it.
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Errorf("the agent home was created: %v", err)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	cases := []struct {
		name    string
		spec    sandbox.SandboxSpec
		wantErr string
	}{
		{
			name: "a mount, which there is nothing here to mount into",
			spec: sandbox.SandboxSpec{
				Cmd:    []string{"/bin/sh"},
				Agent:  testAgent(),
				Mounts: []sandbox.Mount{{Source: "/home/deep/code", Target: "/workspace", Type: sandbox.MountBind}},
			},
			wantErr: "/home/deep/code",
		},
		{
			name:    "no command, which is the session's entrypoint",
			spec:    sandbox.SandboxSpec{},
			wantErr: "spec.Cmd",
		},
		{
			name: "a spec this runtime can host",
			spec: sandbox.SandboxSpec{Cmd: []string{"/bin/sh"}, Agent: testAgent()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			instanceID, err := d.Create(context.Background(), tc.spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if instanceID == "" {
					t.Error("no instance id came back")
				}
				return
			}
			if err == nil {
				t.Fatal("the spec was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestCreate_SkipsATakenSocketName(t *testing.T) {
	d := newTestDriver(t)
	first := instanceOf(t, d, created(t, d))
	// Left behind by a previous run, with no instance to own it.
	stale := filepath.Join(d.socketDir, "AAAAAAAA.sock")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for range 20 {
		// One socket is one tmux server, so the same name twice is two sessions in one server.
		if inst := instanceOf(t, d, created(t, d)); inst.socket == first.socket || inst.socket == stale {
			t.Fatalf("handed out a socket already in use: %s", inst.socket)
		}
	}
}

func TestCreate_SocketPathStaysShort(t *testing.T) {
	d := newTestDriver(t)
	inst := instanceOf(t, d, created(t, d))

	if filepath.Dir(inst.socket) != d.socketDir {
		t.Errorf("socket %q is not in %q", inst.socket, d.socketDir)
	}
	// 108 is the kernel's limit on Linux, 104 on macOS. Anything near it is a failure waiting for a
	// longer home directory, which is how the first droplet run failed.
	if len(inst.socket) > 100 {
		t.Errorf("socket path is %d bytes: %s", len(inst.socket), inst.socket)
	}
}

func TestStop(t *testing.T) {
	cases := []struct {
		name string
		// arrange puts this instance's socket in the state the case is about.
		arrange func(t *testing.T, inst *instance)
	}{
		{
			name: "a socket its server already left behind",
			arrange: func(t *testing.T, inst *instance) {
				if _, err := exec.LookPath("tmux"); err != nil {
					t.Skip("tmux is not installed here")
				}
				deadSocket(t, inst.socket)
			},
		},
		{
			name: "no socket at all, since the agent never started",
			arrange: func(t *testing.T, inst *instance) {
				if _, err := os.Stat(inst.socket); !os.IsNotExist(err) {
					t.Fatalf("this instance has a socket already: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			instanceID := created(t, d)
			tc.arrange(t, instanceOf(t, d, instanceID))

			if err := d.Stop(context.Background(), instanceID); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		})
	}
}

func TestDestroy(t *testing.T) {
	d := newTestDriver(t)
	work := t.TempDir()
	mine := filepath.Join(work, "code.txt")
	if err := os.WriteFile(mine, []byte("yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Cmd:       []string{"/bin/sh"},
		Agent:     testAgent(),
		SessionID: "s-destroy",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := d.Destroy(context.Background(), instanceID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("destroy touched the work folder: %v", err)
	}
}

func TestDestroy_KeepsASocketItCannotKill(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)
	inst := instanceOf(t, d, instanceID)
	deadSocket(t, inst.socket)
	d.killServer = func(context.Context, string) error { return errors.New("still running") }

	if err := d.Destroy(context.Background(), instanceID); err == nil {
		t.Error("Destroy reported success while the agent was still up")
	}
	// Removing any of these would leave an agent running that nothing can reach or stop.
	if _, err := os.Stat(inst.socket); err != nil {
		t.Errorf("the socket was removed: %v", err)
	}
	if _, err := d.lookup(instanceID); err != nil {
		t.Errorf("the session was forgotten while its agent was still up: %v", err)
	}
	if _, err := os.Stat(inst.dir); err != nil {
		t.Errorf("the folder the live tmux server works in was removed: %v", err)
	}

	d.killServer = func(context.Context, string) error { return nil }
	if err := d.Destroy(context.Background(), instanceID); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if _, err := d.lookup(instanceID); err == nil {
		t.Error("the session is still here after a destroy that worked")
	}
}

func TestExec(t *testing.T) {
	cases := []struct {
		name string
		// arrange makes the instance and says what to run, and what its output should be.
		arrange  func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string)
		suffix   bool
		wantCode int
	}{
		{
			name: "stdout",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				return created(t, d), sandbox.ExecSpec{Cmd: []string{"echo", "hello"}}, "hello"
			},
		},
		{
			name: "an exit code",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				return created(t, d), sandbox.ExecSpec{Cmd: []string{"sh", "-c", "exit 3"}}, ""
			},
			wantCode: 3,
		},
		{
			name: "the working directory it was given",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				work := t.TempDir()
				return created(t, d), sandbox.ExecSpec{Cmd: []string{"pwd"}, WorkingDir: work}, work
			},
			// macOS hands out /var paths that resolve to /private/var.
			suffix: true,
		},
		{
			name: "the user's own home, where the agent's login and dotfiles are",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				return created(t, d), sandbox.ExecSpec{Cmd: []string{"sh", "-c", "echo $HOME"}}, os.Getenv("HOME")
			},
		},
		{
			name: "a tmux that cannot reach the user's own server",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
				t.Setenv("TMUX_PANE", "%7")
				t.Setenv("PROMPT_COMMAND", "starship_precmd")
				instanceID := created(t, d)
				inst := instanceOf(t, d, instanceID)
				spec := sandbox.ExecSpec{Cmd: []string{"sh", "-c",
					`echo "${TMUX_TMPDIR}|${TMUX-unset}|${TMUX_PANE-unset}|${PROMPT_COMMAND-unset}"`}}
				return instanceID, spec, inst.dir + "|unset|unset|unset"
			},
		},
		{
			name: "none of deepcove's own secrets",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				t.Setenv("TS_AUTHKEY", "tskey-auth-secret")
				t.Setenv("DEEPCOVE_TOKEN", "a-token")
				spec := sandbox.ExecSpec{Cmd: []string{"sh", "-c",
					`echo "${TS_AUTHKEY-gone}|${DEEPCOVE_TOKEN-gone}|${PATH:+path}"`}}
				return created(t, d), spec, "gone|gone|path"
			},
		},
		{
			name: "the call's environment over the instance's",
			arrange: func(t *testing.T, d *Driver) (sandbox.ID, sandbox.ExecSpec, string) {
				instanceID, err := d.Create(context.Background(), sandbox.SandboxSpec{
					Cmd: []string{"/bin/sh"},
					Env: map[string]string{"DEEPCOVE_PROBE": "from the spec", "ONLY_FROM_SPEC": "yes"},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if err := d.Start(context.Background(), instanceID); err != nil {
					t.Fatalf("Start: %v", err)
				}
				spec := sandbox.ExecSpec{
					Cmd: []string{"sh", "-c", "echo $DEEPCOVE_PROBE/$ONLY_FROM_SPEC"},
					Env: map[string]string{"DEEPCOVE_PROBE": "from the call"},
				}
				return instanceID, spec, "from the call/yes"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			instanceID, spec, want := tc.arrange(t, d)

			p, err := d.Exec(context.Background(), instanceID, spec)
			if err != nil {
				t.Fatalf("Exec: %v", err)
			}
			got := readAll(t, p.Stdout())
			code, err := p.Wait(context.Background())
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if code != tc.wantCode {
				t.Errorf("exit code %d, want %d", code, tc.wantCode)
			}
			switch {
			case want == "":
			case tc.suffix && !strings.HasSuffix(got, want):
				t.Errorf("got %q, want it to end with %q", got, want)
			case !tc.suffix && got != want:
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestExec_OnAStoppedSession(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)
	if err := d.Stop(context.Background(), instanceID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Every other runtime refuses this because its machine is down. Here the machine is the user's, so
	// the refusal has to be explicit.
	_, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{
		Cmd: []string{"touch", filepath.Join(t.TempDir(), "ran")},
	})
	if !errors.Is(err, sandbox.ErrConflict) {
		t.Errorf("Exec: %v, want ErrConflict", err)
	}
}

func TestExec_WithATTY(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)

	p, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{Cmd: []string{"tty"}, Tty: true})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// No error check: a pty master reports EIO on Linux when the child lets go of the other end.
	out, _ := io.ReadAll(p.Stdout())
	if got := strings.TrimSpace(string(out)); !strings.Contains(got, "/dev/") {
		t.Errorf("tty said %q, want a terminal device", got)
	}
}

func TestProcessResize(t *testing.T) {
	cases := []struct {
		name    string
		tty     bool
		wantErr error
	}{
		{name: "with a terminal", tty: true},
		{name: "without one", wantErr: sandbox.ErrNotSupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			instanceID := created(t, d)
			cmd := []string{"true"}
			if tc.tty {
				cmd = []string{"sleep", "5"}
			}

			p, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{Cmd: cmd, Tty: tc.tty})
			if err != nil {
				t.Fatalf("Exec: %v", err)
			}
			defer func() { _ = p.Close() }()

			if err := p.Resize(100, 40); !errors.Is(err, tc.wantErr) {
				t.Errorf("Resize: %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestProcessClose(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)

	p, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{Cmd: []string{"sleep", "60"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// The group, not the process: a shell would otherwise leave its children behind.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after Close: %v", err)
	}
}

func TestListSandboxes(t *testing.T) {
	d := newTestDriver(t)
	instanceID := created(t, d)

	running, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(running) != 1 || running[0].State != sandbox.InstanceRunning {
		t.Fatalf("got %v, want one running instance", running)
	}

	if err := d.Destroy(context.Background(), instanceID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	left, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d instances left after destroy", len(left))
	}
	if _, err := d.Exec(context.Background(), instanceID, sandbox.ExecSpec{Cmd: []string{"true"}}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("exec on a destroyed instance: got %v, want ErrNotFound", err)
	}
}

func TestPing(t *testing.T) {
	d := newTestDriver(t)
	version, err := d.Ping(context.Background())
	if err != nil {
		t.Skip("tmux is not installed here, which Ping is right to report")
	}
	// The nodes page prints this as the runtime's version, so anything here reads as one.
	if version != "" {
		t.Errorf("Ping reported the version %q, and this runtime has none", version)
	}
}

func TestUnsupportedCalls(t *testing.T) {
	cases := []struct {
		name string
		call func(d *Driver, instanceID sandbox.ID) error
	}{
		{
			name: "Logs",
			call: func(d *Driver, instanceID sandbox.ID) error {
				_, err := d.Logs(context.Background(), instanceID, sandbox.LogOpts{})
				return err
			},
		},
		{
			name: "Snapshot",
			call: func(d *Driver, instanceID sandbox.ID) error {
				_, err := d.Snapshot(context.Background(), instanceID)
				return err
			},
		},
		{
			name: "CopyIn",
			call: func(d *Driver, instanceID sandbox.ID) error {
				return d.CopyIn(context.Background(), instanceID, "/from", "/to")
			},
		},
		{
			name: "CopyOut",
			call: func(d *Driver, instanceID sandbox.ID) error {
				return d.CopyOut(context.Background(), instanceID, "/from", "/to")
			},
		},
	}
	d := newTestDriver(t)
	instanceID := created(t, d)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(d, instanceID); !errors.Is(err, sandbox.ErrNotSupported) {
				t.Errorf("got %v, want ErrNotSupported", err)
			}
		})
	}
}

func TestWithTmuxSocket(t *testing.T) {
	// Handing tmux no socket killed a real tmux server twice, before this was explicit.
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "a bare tmux command",
			argv: []string{"tmux", "kill-server"},
			want: []string{"tmux", "-S", "/s/tmux.sock", "kill-server"},
		},
		{
			name: "tmux by absolute path",
			argv: []string{"/usr/bin/tmux", "ls"},
			want: []string{"/usr/bin/tmux", "-S", "/s/tmux.sock", "ls"},
		},
		{
			name: "anything else is left alone",
			argv: []string{"sh", "-c", "tmux ls"},
			want: []string{"sh", "-c", "tmux ls"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withTmuxSocket(tc.argv, "/s/tmux.sock")
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
