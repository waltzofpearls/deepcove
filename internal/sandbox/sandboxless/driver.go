//go:build !windows

// Package sandboxless runs a session directly on the machine, with no VM and no container.
//
// Nothing here isolates anything. The agent runs as the user deepcove runs as, with that user's files,
// network and credentials. It is for a machine that is already the boundary, such as a cloud instance
// you set aside for this, where a second layer buys nothing.
//
// A session outlives deepcove: its agent runs in a tmux server on this machine. What this driver
// tracks lives in memory, so at startup it is told which sandbox ids its sessions had, takes those
// back, and stops whatever else it left running.
package sandboxless

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

const idPrefix = "sandboxless-"

// socketsDirName is a folder of our own inside the ipc directory, which the VM runtime also writes
// sockets into. The sweep stops the server behind every socket it finds in ours.
const socketsDirName = "tmux"

// Driver implements sandbox.Sandbox by running processes on this machine.
type Driver struct {
	stateDir  string
	agentHome string
	// socketDir holds the tmux sockets, in the runtime directory rather than under the data dir, where
	// the path would be too long for one. See shortID.
	socketDir string
	// installAgent, killServer and serverAlive are fields so tests can put an agent in place without the
	// network, and can answer for a tmux server without one to break.
	installAgent func(ctx context.Context, agentHome, install string) (string, error)
	killServer   func(ctx context.Context, socket string) error
	serverAlive  func(ctx context.Context, socket string) (bool, error)
	// tmuxCallBudget is a field for the same reason, so a test waits milliseconds for a tmux that never
	// answers rather than seconds.
	tmuxCallBudget time.Duration
	adoptBudget    time.Duration

	// installMu keeps two sessions from installing into the shared agent home at the same time. It is
	// held while agentMu is taken, never the other way round.
	installMu sync.Mutex
	// agentMu guards agentPaths, which remembers where each agent binary was found. Its own lock, so
	// the create form asking whether an agent is here never waits on a session being made.
	agentMu    sync.Mutex
	agentPaths map[string]string

	mu        sync.Mutex
	instances map[sandbox.ID]*instance
}

type instance struct {
	spec      sandbox.SandboxSpec
	dir       string
	socket    string
	running   bool
	createdAt time.Time
	setup     sandbox.SetupState
	// agentPath is where the agent was found, which the launch script names.
	agentPath string
	// cancelSetup stops the install when the session goes before it finishes.
	cancelSetup context.CancelFunc
}

// NewDriver keeps its instances and its sessions' folders under stateDir, their tmux sockets in a
// folder of its own under ipcDir, and installs an agent this machine does not already have into
// agentHome.
func NewDriver(stateDir, agentHome, ipcDir string) (*Driver, error) {
	if stateDir == "" || agentHome == "" || ipcDir == "" {
		return nil, fmt.Errorf("sandboxless: a state directory, an agent home and an ipc directory are required")
	}
	if err := requireWritableAgentHome(agentHome); err != nil {
		return nil, fmt.Errorf("sandboxless: %w", err)
	}
	socketDir := filepath.Join(ipcDir, socketsDirName)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return nil, fmt.Errorf("sandboxless: the socket folder: %w", err)
	}
	return &Driver{
		stateDir:       stateDir,
		agentHome:      agentHome,
		socketDir:      socketDir,
		installAgent:   runInstall,
		killServer:     killTmuxServer,
		serverAlive:    tmuxServerAlive,
		tmuxCallBudget: tmuxCallBudget,
		adoptBudget:    adoptBudget,
		instances:      map[sandbox.ID]*instance{},
		agentPaths:     map[string]string{},
	}, nil
}

// requireWritableAgentHome checks an agent home that is already there, and leaves a node that has none
// alone: the first install makes it.
func requireWritableAgentHome(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return proveWritable(path)
}

// proveWritable writes a file, because asking the kernel misses a full disk.
func proveWritable(path string) error {
	probe := filepath.Join(path, ".deepcove-write-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

var (
	_ sandbox.Sandbox     = (*Driver)(nil)
	_ sandbox.Pinger      = (*Driver)(nil)
	_ sandbox.Lister      = (*Driver)(nil)
	_ sandbox.Adopter     = (*Driver)(nil)
	_ sandbox.ForceCloser = (*Driver)(nil)
)

// Create records a session's folder and tmux socket; nothing runs until Start. A refused share is a plain
// sentence for the user; other errors carry the "sandboxless:" prefix.
func (d *Driver) Create(_ context.Context, spec sandbox.SandboxSpec) (sandbox.ID, error) {
	// This driver never runs Cmd, but every spec the session layer builds has one, so an empty Cmd means a
	// spec built somewhere else, which would also be missing the agent and its folder.
	if len(spec.Cmd) == 0 {
		return "", fmt.Errorf("sandboxless: spec.Cmd is empty, so the spec did not come from the session layer")
	}
	if len(spec.Mounts) > 0 {
		return "", fmt.Errorf("a session with no sandbox cannot share %s: the agent already works on this machine's own files",
			spec.Mounts[0].Source)
	}
	instanceID, socket, err := d.mintID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(d.instancesDir(), string(instanceID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sandboxless: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.instances[instanceID] = &instance{
		spec:      spec,
		dir:       dir,
		socket:    socket,
		createdAt: time.Now(),
	}
	return instanceID, nil
}

// instancesDir holds a folder per session: its launch script, and what TMUX_TMPDIR points at so a tmux
// the agent starts itself lands there rather than on the user's own server.
func (d *Driver) instancesDir() string {
	return filepath.Join(d.stateDir, "instances")
}

// mintID returns an id whose socket name nobody else is using. Two sessions on one socket would be two
// sessions in one tmux server.
func (d *Driver) mintID() (sandbox.ID, string, error) {
	for range 100 {
		instanceID := sandbox.ID(idPrefix + rand.Text())
		socket := filepath.Join(d.socketDir, shortID(instanceID)+".sock")
		if d.socketTaken(socket) {
			continue
		}
		return instanceID, socket, nil
	}
	return "", "", fmt.Errorf("sandboxless: no free socket name in %s", d.socketDir)
}

// socketTaken covers both a live instance and a socket left behind by a previous run.
func (d *Driver) socketTaken(socket string) bool {
	if d.socketHasAnInstance(socket) {
		return true
	}
	_, err := os.Stat(socket)
	return err == nil
}

func (d *Driver) socketHasAnInstance(socket string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, inst := range d.instances {
		if inst.socket == socket {
			return true
		}
	}
	return false
}

func (d *Driver) hasInstance(instanceID sandbox.ID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.instances[instanceID]
	return ok
}

// Start has no machine to boot. It puts the agent in place instead, in the background, and the session
// layer waits on Setup before it starts anything. A start with the agent already here does nothing.
func (d *Driver) Start(_ context.Context, instanceID sandbox.ID) error {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if inst.running {
		d.mu.Unlock()
		return fmt.Errorf("the session is already running: %w", sandbox.ErrConflict)
	}
	inst.running = true
	// The launch script names the path the agent was found at, so one that has gone since is worth
	// looking for again rather than opening a window on nothing. A setup that failed retries here too.
	provision := inst.setup.Status != sandbox.SetupInstalling && !isExecutable(inst.agentPath)
	var setupCtx context.Context
	if provision {
		inst.setup = sandbox.SetupState{Status: sandbox.SetupInstalling}
		setupCtx, inst.cancelSetup = context.WithTimeout(context.Background(), installTimeout)
	}
	d.mu.Unlock()

	if provision {
		go d.provisionAgent(setupCtx, instanceID, inst)
	}
	return nil
}

// Stop kills this sandboxless instance's tmux server, which takes the agent and anything it
// started with it.
func (d *Driver) Stop(ctx context.Context, instanceID sandbox.ID) error {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return err
	}
	// Only when this sandboxless instance's own socket is there. Without one, tmux falls back to
	// the user's default socket, and kill-server would take down the tmux they are sitting in.
	if _, err := os.Stat(inst.socket); err == nil {
		if err := d.killServer(ctx, inst.socket); err != nil {
			return fmt.Errorf("sandboxless: %s: %w", instanceID, err)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// The install runs whether or not there is an agent to stop yet, and a stopped session should have
	// nothing of its own still going. Start looks for the agent again.
	if inst.cancelSetup != nil {
		inst.cancelSetup()
	}
	// Only what was cancelled. An agent already installed stays installed, and a start finds it ready.
	if inst.setup.Status == sandbox.SetupInstalling {
		inst.setup = sandbox.SetupState{}
	}
	inst.running = false
	return nil
}

func (d *Driver) Destroy(ctx context.Context, instanceID sandbox.ID) error {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return err
	}
	// The agent is still up, so this session is not gone. Nothing below runs.
	if err := d.Stop(ctx, instanceID); err != nil {
		return err
	}

	d.mu.Lock()
	if inst.cancelSetup != nil {
		inst.cancelSetup()
	}
	delete(d.instances, instanceID)
	d.mu.Unlock()

	socketErr := os.Remove(inst.socket)
	if os.IsNotExist(socketErr) {
		socketErr = nil
	}
	return errors.Join(socketErr, os.RemoveAll(inst.dir))
}

// Logs has nothing to read. A session's output lives in its tmux pane, which the terminal shows.
func (d *Driver) Logs(context.Context, sandbox.ID, sandbox.LogOpts) (io.ReadCloser, error) {
	return nil, sandbox.ErrNotSupported
}

// CopyIn and CopyOut have nobody to copy for: no caller in the tree uses them, and both ends here are
// the same filesystem anyway.
func (d *Driver) CopyIn(context.Context, sandbox.ID, string, string) error {
	return sandbox.ErrNotSupported
}

func (d *Driver) CopyOut(context.Context, sandbox.ID, string, string) error {
	return sandbox.ErrNotSupported
}

func (d *Driver) Snapshot(context.Context, sandbox.ID) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (d *Driver) Restore(context.Context, sandbox.ID, sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// RemoveStorage deletes the folder this driver made for a session. A session working in a folder you
// picked has nothing here, and that path never reaches this code: the only path it can build is its own.
func (d *Driver) RemoveStorage(_ context.Context, sessionID string) error {
	dir, err := d.workspaceToRemove(sessionID)
	if err != nil {
		return err
	}
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("sandboxless: remove the session folder: %w", err)
	}
	return nil
}

// Ping fails when tmux is missing, the one thing this runtime needs and cannot install itself. It
// reports no version, because there is no backend here to have one.
func (d *Driver) Ping(context.Context) (string, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return "", fmt.Errorf("sandboxless: tmux is not installed: %w", sandbox.ErrUnavailable)
	}
	return "", nil
}

func (d *Driver) ListSandboxes(context.Context) ([]sandbox.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]sandbox.Instance, 0, len(d.instances))
	for instanceID, inst := range d.instances {
		state := sandbox.InstanceStopped
		if inst.running {
			state = sandbox.InstanceRunning
		}
		out = append(out, sandbox.Instance{
			ID:        instanceID,
			State:     state,
			Image:     inst.spec.Image,
			Mounts:    inst.spec.Mounts,
			CreatedAt: inst.createdAt,
		})
	}
	return out, nil
}

// Close stops any install still running. The agents stay up: they are tmux servers on this machine,
// not something deepcove holds, and the next start takes them back.
func (d *Driver) Close() error {
	return d.ForceClose()
}

// ForceClose is the same: nothing here has to be torn down for deepcove to exit.
func (d *Driver) ForceClose() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, inst := range d.instances {
		if inst.cancelSetup != nil {
			inst.cancelSetup()
		}
	}
	return nil
}

func (d *Driver) lookup(instanceID sandbox.ID) (*instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inst, ok := d.instances[instanceID]
	if !ok {
		return nil, fmt.Errorf("sandboxless: unknown instance %s: %w", instanceID, sandbox.ErrNotFound)
	}
	return inst, nil
}

// shortID names the socket. A unix socket path is capped near 108 bytes, which the id under a data dir
// would blow, so the name is the first eight characters of it. Eight can clash, and mintID retries.
func shortID(instanceID sandbox.ID) string {
	trimmed := strings.TrimPrefix(string(instanceID), idPrefix)
	if len(trimmed) > 8 {
		return trimmed[:8]
	}
	return trimmed
}
