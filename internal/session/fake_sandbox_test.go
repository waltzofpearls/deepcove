package session

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// fakeSandbox is an in-memory sandbox.Sandbox for tests. It records
// every call and lets each lifecycle method be made to fail via a
// hook, which is what the rollback-on-partial-failure tests need.
type fakeSandbox struct {
	// The agent answers a guest runtime shares, which is what this fake stands in for.
	sandbox.SandboxedProvisioner
	mu sync.Mutex

	created   map[sandbox.ID]sandbox.SandboxSpec
	started   map[sandbox.ID]bool
	stopped   map[sandbox.ID]bool
	destroyed map[sandbox.ID]bool

	// Calls tracks the order of lifecycle operations for tests that
	// care about the sequence (e.g. rollback ordering).
	calls []string

	// Hooks let an individual test inject a failure for a specific
	// method. Nil means "succeed".
	createErr  error
	startErr   error
	stopErr    error
	destroyErr error

	// execResponses is a FIFO queue of canned Exec results consumed
	// in order. Test setup pushes one entry per expected Exec call;
	// asserting on execCalls verifies the cmds the service issued.
	execResponses []execResponse
	execCalls     [][]string

	// removedStorage records every RemoveStorage call argument in
	// order. Used by destroy + edit-mounts tests to assert the
	// agent-home volume cleanup actually fires.
	removedStorage   []string
	removeStorageErr error

	// specs records every SandboxSpec passed to Create, in order. Lets entrypoint /
	// MOTD / mount-layout tests assert on what the service actually built without
	// reaching into the service's internals.
	specs []sandbox.SandboxSpec

	// Optional Pinger capability: preflight tests set pingErr to
	// simulate a down daemon; pingVersion is returned when reachable.
	pingErr     error
	pingVersion string
}

// execResponse scripts a single Exec invocation. Set err to make
// Exec itself fail (driver-level error, no process). Otherwise the
// returned process serves stdout/stderr and exits with `exit`.
type execResponse struct {
	stdout string
	stderr string
	exit   int
	err    error
}

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{
		created:   map[sandbox.ID]sandbox.SandboxSpec{},
		started:   map[sandbox.ID]bool{},
		stopped:   map[sandbox.ID]bool{},
		destroyed: map[sandbox.ID]bool{},
	}
}

var (
	_ sandbox.Sandbox = (*fakeSandbox)(nil)
	_ sandbox.Pinger  = (*fakeSandbox)(nil)
)

// Ping implements sandbox.Pinger for preflight tests.
func (f *fakeSandbox) Ping(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Ping")
	if f.pingErr != nil {
		return "", f.pingErr
	}
	return f.pingVersion, nil
}

func (f *fakeSandbox) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Create")
	f.specs = append(f.specs, spec)
	if f.createErr != nil {
		return "", f.createErr
	}
	h := sandbox.ID(uuid.NewString())
	f.created[h] = spec
	return h, nil
}

func (f *fakeSandbox) Start(ctx context.Context, h sandbox.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Start")
	if f.startErr != nil {
		return f.startErr
	}
	if _, ok := f.created[h]; !ok {
		return sandbox.ErrNotFound
	}
	f.started[h] = true
	return nil
}

func (f *fakeSandbox) Stop(ctx context.Context, h sandbox.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Stop")
	if f.stopErr != nil {
		return f.stopErr
	}
	if _, ok := f.created[h]; !ok {
		return sandbox.ErrNotFound
	}
	f.stopped[h] = true
	delete(f.started, h)
	return nil
}

func (f *fakeSandbox) Destroy(ctx context.Context, h sandbox.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Destroy")
	if f.destroyErr != nil {
		return f.destroyErr
	}
	if _, ok := f.created[h]; !ok {
		return sandbox.ErrNotFound
	}
	f.destroyed[h] = true
	delete(f.created, h)
	delete(f.started, h)
	delete(f.stopped, h)
	return nil
}

func (f *fakeSandbox) Exec(ctx context.Context, h sandbox.ID, cmd sandbox.ExecSpec) (sandbox.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, append([]string(nil), cmd.Cmd...))
	if len(f.execResponses) == 0 {
		// Creating a session now also starts its agent, three tmux calls that almost no test is about.
		// Answer those when nothing is scripted, so a test only scripts what it actually asserts on.
		// Everything else still fails loudly, which is what catches an exec nobody expected.
		if isAgentStartCmd(cmd.Cmd) {
			return newScriptedProcess("", "", 0), nil
		}
		return nil, errors.New("fakeSandbox: Exec called with no scripted response left (cmd=" + strings.Join(cmd.Cmd, " ") + ")")
	}
	resp := f.execResponses[0]
	f.execResponses = f.execResponses[1:]
	if resp.err != nil {
		return nil, resp.err
	}
	return newScriptedProcess(resp.stdout, resp.stderr, resp.exit), nil
}

// scriptedProcess is a sandbox.Process driven by canned stdout / stderr / exit code; used by
// windows_test.go to drive the tmux exec helpers without a real container.
type scriptedProcess struct {
	stdout *strings.Reader
	stderr *strings.Reader
	exit   int
}

func newScriptedProcess(stdout, stderr string, exit int) *scriptedProcess {
	return &scriptedProcess{
		stdout: strings.NewReader(stdout),
		stderr: strings.NewReader(stderr),
		exit:   exit,
	}
}

var _ sandbox.Process = (*scriptedProcess)(nil)

func (p *scriptedProcess) Stdout() io.ReadCloser                 { return io.NopCloser(p.stdout) }
func (p *scriptedProcess) Stderr() io.ReadCloser                 { return io.NopCloser(p.stderr) }
func (p *scriptedProcess) Stdin() io.WriteCloser                 { return nopWriteCloser{} }
func (p *scriptedProcess) Wait(ctx context.Context) (int, error) { return p.exit, nil }
func (p *scriptedProcess) Signal(s os.Signal) error              { return sandbox.ErrNotSupported }
func (p *scriptedProcess) Resize(cols, rows uint16) error        { return nil }
func (p *scriptedProcess) Close() error                          { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func (f *fakeSandbox) Logs(ctx context.Context, h sandbox.ID, opts sandbox.LogOpts) (io.ReadCloser, error) {
	return nil, sandbox.ErrNotSupported
}

func (f *fakeSandbox) CopyIn(ctx context.Context, h sandbox.ID, src, dst string) error {
	return sandbox.ErrNotSupported
}

func (f *fakeSandbox) CopyOut(ctx context.Context, h sandbox.ID, src, dst string) error {
	return sandbox.ErrNotSupported
}

func (f *fakeSandbox) Snapshot(ctx context.Context, h sandbox.ID) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (f *fakeSandbox) Restore(ctx context.Context, h sandbox.ID, id sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// removedStorage records volume names passed to RemoveStorage so
// EditMounts and Destroy tests can verify the agent-home cleanup
// path actually fires. Driver-level mock concerns (the call
// happened) belong here; whether Docker actually deleted the volume
// is the driver's contract, not session.Service's.
func (f *fakeSandbox) RemoveStorage(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "RemoveStorage")
	f.removedStorage = append(f.removedStorage, name)
	if f.removeStorageErr != nil {
		return f.removeStorageErr
	}
	return nil
}

// liveCount is a test helper: how many sandboxes are still "alive"
// (created but not destroyed). Used by rollback tests to assert no
// orphans remain.
// Setup reads the files the tests serve through Exec, the way a guest runtime reads its own.
func (f *fakeSandbox) Setup(ctx context.Context, sandboxID sandbox.ID) (sandbox.SetupState, error) {
	return sandbox.SandboxedSetup(ctx, f, sandboxID)
}

func (f *fakeSandbox) liveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

// callSequence joins the recorded calls for easy assertion.
func (f *fakeSandbox) callSequence() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

// pathExists is a small helper used by tests to verify workspace
// rollback. Stays in this file rather than test_helpers.go so the
// session package keeps a flat layout while it's small.
func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isAgentStartCmd reports whether cmd is one of the tmux calls startAgentSession makes.
func isAgentStartCmd(cmd []string) bool {
	if len(cmd) < 2 || cmd[0] != "tmux" {
		return false
	}
	switch cmd[1] {
	case "has-session", "new-session", "set-option":
		return true
	}
	return false
}
