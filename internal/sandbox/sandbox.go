// Package sandbox abstracts the runtime layer that hosts coding-agent sessions. Drivers live in
// sibling subpackages (e.g. sandbox/vm) so each backend's dependencies stay out of callers that only
// need the interface, the same pattern as database/sql vs. database/sql/driver.
//
// Design highlights:
//   - The interface accommodates per-session VMs (Cloud Hypervisor, Apple Virtualization.framework)
//     as well as running on the machine itself (sandboxless). Snapshot/Restore are optional and
//     return ErrNotSupported on backends that lack them.
//   - SandboxSpec.Mounts only ever points at host paths this node owns. Sessions never cross host
//     boundaries. The VM driver enforces this with ValidateSpec; sandboxless takes no mounts.
//   - Tailscale membership (tsnet) is the node's, not the sandbox's.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Sentinel errors. Drivers normalize their native error types into
// these so the session layer can react portably:
//
//   - ErrNotSupported: the operation isn't implemented by this driver
//     (e.g. Snapshot on sandboxless). Treat as a perf-only signal.
//   - ErrNotFound: the id doesn't refer to a live sandbox (e.g.
//     someone destroyed it out of band). Stop/Destroy can treat this
//     as idempotent success.
//   - ErrConflict: the operation can't be performed in the current
//     state (e.g. Start on an already-running sandbox).
var (
	ErrNotSupported = errors.New("sandbox: operation not supported by this driver")
	ErrNotFound     = errors.New("sandbox: not found")
	ErrConflict     = errors.New("sandbox: lifecycle conflict")

	// ErrUnavailable means the backend itself is unreachable. Distinct from ErrNotFound (the backend
	// is up but the sandbox is absent). The session layer maps this to a 503 with a plain message
	// instead of a raw error.
	ErrUnavailable = errors.New("sandbox: backend unavailable")

	// ErrImageUnavailable means the image could not be fetched: a wrong name, a private image, or no
	// way to reach the registry. Nothing about the session itself is wrong.
	ErrImageUnavailable = errors.New("sandbox: image unavailable")
)

// Pinger is an optional capability: a driver that can cheaply check
// whether its backend is reachable implements it. The session layer
// uses it for the GET /api/preflight environment check. Drivers that
// don't implement it are treated as "readiness unknown."
type Pinger interface {
	// Ping reports backend liveness. The returned string is a
	// human-meaningful version identifier when reachable; it's
	// best-effort and may be empty.
	// A non-nil error (typically wrapping ErrUnavailable) means the
	// backend is unreachable.
	Ping(ctx context.Context) (version string, err error)
}

// ImageManager is an optional capability for backends that build sessions from an OCI image, so the
// session layer can say whether a create has a download ahead of it. A backend without images, like
// sandboxless, skips the image-readiness path.
type ImageManager interface {
	// ImageExists reports whether ref is already present locally.
	ImageExists(ctx context.Context, ref string) (bool, error)
	// ImagePull fetches ref from its registry, blocking until the pull
	// completes. onProgress, if non-nil, is invoked with aggregate
	// progress updates during the pull (for a UI progress bar); pass
	// nil for a silent pull.
	ImagePull(ctx context.Context, ref string, onProgress func(PullProgress)) error
}

// PullProgress is a backend-agnostic image-pull progress update.
// Current/Total are aggregate bytes across all layers; Total is 0 early
// in the pull before layer sizes are known (the UI shows an
// indeterminate bar until then).
type PullProgress struct {
	Status  string // short phase, e.g. "Downloading", "Extracting"
	Current int64
	Total   int64
}

// ForceCloser abandons its sandboxes instead of stopping them politely. Exiting on top of a VM the
// hypervisor still owns leaves a process the kernel cannot reap.
type ForceCloser interface {
	ForceClose() error
}

// Adopter is a runtime whose sandboxes outlive deepcove and can be taken back at startup.
type Adopter interface {
	// Adopt takes back the sandboxes it can find among these, stops whatever else it left behind, and
	// returns the ones it took. Each spec is that sandbox's session record rebuilt, which is what the
	// previous run created it with.
	Adopt(ctx context.Context, sandboxes map[ID]SandboxSpec) (taken []ID, err error)
}

// Lister is an optional capability for drivers that can enumerate what they are actually hosting,
// independent of what the session table believes. That gap is the whole point: comparing the two is how
// you tell a leaked sandbox from a session whose sandbox is already gone.
//
// It matters most for VM backends: only deepcove knows which VM belongs to which session. Drivers that
// cannot enumerate their sandboxes simply do not implement this, and the session layer reports listing as
// unsupported for them.
type Lister interface {
	// ListSandboxes returns every sandbox this driver currently tracks, running or not.
	ListSandboxes(ctx context.Context) ([]Instance, error)
}

// Restarter is a runtime that can stop and start a sandbox in place, picking up a new spec. A VM reads
// its size and mounts at boot, so this applies changes without losing the sandbox.
type Restarter interface {
	Restart(ctx context.Context, sandboxID ID, spec SandboxSpec) error
}

// SpecUpdater is a runtime whose sandbox boots from a stored spec. UpdateSpec replaces that spec for the
// next boot and leaves a running sandbox as it is, so a change saved for later reaches a plain Start
// too, not only a restart.
type SpecUpdater interface {
	UpdateSpec(ctx context.Context, sandboxID ID, spec SandboxSpec) error
}

// LLMForwarder is a runtime that carries a sandbox's LLM traffic itself, so the sandbox never reaches
// the gateway over the network and the request carries this node's identity.
type LLMForwarder interface {
	// LLMEndpoint is the URL a sandbox calls instead of the provider.
	LLMEndpoint() string
}

// AgentProvisioner is how a runtime answers for the agent it hosts. Every runtime implements it: a
// guest runtime reads the files its entrypoint wrote, and one with no guest answers from its own state.
type AgentProvisioner interface {
	// Setup is how far this sandbox has got with getting the agent ready.
	Setup(ctx context.Context, sandboxID ID) (SetupState, error)
	// AgentLaunchCommand is what the terminal multiplexer runs to start agent, which matters on a machine
	// that runs more than one.
	AgentLaunchCommand(sandboxID ID, agent string) ([]string, error)
	// WorkDir is the folder the agent works in. Keyed by session, not sandbox, because the spec that
	// names it is built before the sandbox exists.
	WorkDir(sessionID string, mounts []Mount) string
	// WorkspaceRoot is where a runtime that makes folders for sessions keeps them, and empty for one
	// that does not. The create form shows it before a session exists.
	WorkspaceRoot() string
	// AgentInstalled reports whether the agent is already there, so the create form can say whether
	// picking it means installing it first. A sandboxed runtime installs it at boot either way.
	AgentInstalled(agent Agent) bool
}

// Agent is which coding agent a sandbox hosts, and for a runtime with no guest, how to put it there.
//
// Name is all a guest runtime needs, since its image brings the agent. The rest is for a runtime that
// installs the agent on the machine itself.
type Agent struct {
	Name string
	// Binary is what to look for on PATH and in the agent home.
	Binary string
	// Install runs only when there is no binary, with HOME set to the agent home.
	Install string
	// LaunchCmd starts the agent and restarts it when it exits. It runs the binary at $DEEPCOVE_AGENT,
	// which the runtime sets to the path it resolved.
	LaunchCmd string
}

// SetupStatus is how far the agent's setup has got. Its own type, so the guest's word for the same
// idea, which differs (see SandboxedStatusErrored), cannot be assigned here by mistake.
type SetupStatus string

// Setup states, reported by a runtime that installs the agent itself.
const (
	// SetupInstalling: the agent is being put in place.
	SetupInstalling SetupStatus = "installing"
	// SetupReady: the agent is there and can be started.
	SetupReady SetupStatus = "ready"
	// SetupFailed: it did not work, and Log says what happened.
	SetupFailed SetupStatus = "failed"
)

// SetupState is how far a runtime has got with preparing the agent, and whatever it printed on the way.
type SetupState struct {
	Status SetupStatus
	Log    string
}

// InstanceState is a sandbox's lifecycle state as the driver sees it, normalized across backends. A
// driver with a richer native vocabulary maps onto these, because the view exists to answer "is this
// thing still holding resources", not to mirror the backend.
type InstanceState string

const (
	// InstanceRunning is executing right now and holding memory and CPU.
	InstanceRunning InstanceState = "running"
	// InstanceStarting has a start in flight: created, not yet answering. A VM spends its whole boot
	// here, which is long enough that reporting it as stopped reads as broken.
	InstanceStarting InstanceState = "starting"
	// InstanceStopped exists but is not executing. Created-but-never-started lands here too: from the
	// outside the two are the same, something tracked that is not running.
	InstanceStopped InstanceState = "stopped"
	// InstanceUnknown is a state the driver reported but we could not map.
	InstanceUnknown InstanceState = "unknown"
)

// Instance is one sandbox as its driver sees it. Deliberately thin: the session layer joins it against
// the session table to produce the view a user reads, so anything already on the session row (name,
// agent, owner) does not belong here.
type Instance struct {
	ID    ID
	State InstanceState
	// Unresponsive means the driver is holding off dials because the guest stopped answering. The
	// instance is still running; nothing inside it can be reached.
	Unresponsive bool
	// Image is the reference the sandbox was built from, for spotting a sandbox left over from an
	// image you no longer use.
	Image string
	// Mounts are the host folders shared into this sandbox, so a view can say what a machine is working
	// on without joining against a session.
	Mounts []Mount
	// Resources are what the sandbox was actually given, which is not always what was asked for: a VM
	// rounds CPU up to whole cores, and a zero field means the driver's own default applied.
	Resources Resources
	// CreatedAt is when the driver first tracked the sandbox. Zero when the driver cannot say.
	CreatedAt time.Time
}

// ID is an opaque, driver-owned reference to a created sandbox. Treat it as a token to pass back
// into the same driver; do not interpret its contents. Drivers may key their own state off it.
type ID string

// Sandbox is the surface every driver implements.
type Sandbox interface {
	// Lifecycle.
	Create(ctx context.Context, spec SandboxSpec) (ID, error)
	Start(ctx context.Context, sandboxID ID) error
	Stop(ctx context.Context, sandboxID ID) error
	Destroy(ctx context.Context, sandboxID ID) error

	// Exec runs a command inside an already-Started sandbox. The returned Process is owned by the
	// caller; call Close on it (or Wait, which closes on completion) to release the underlying
	// connection.
	Exec(ctx context.Context, sandboxID ID, cmd ExecSpec) (Process, error)

	// Logs returns the sandbox's stdout/stderr stream. Follow=true
	// keeps the reader open across new writes; closing the reader
	// is the caller's responsibility.
	Logs(ctx context.Context, sandboxID ID, opts LogOpts) (io.ReadCloser, error)

	// CopyIn / CopyOut move single paths between host and sandbox.
	// Behavior on directories is driver-defined (most copy recursively).
	CopyIn(ctx context.Context, sandboxID ID, src, dst string) error
	CopyOut(ctx context.Context, sandboxID ID, src, dst string) error

	// Snapshot / Restore are optional. Drivers without support
	// return ErrNotSupported and the session layer falls back to
	// cold-creating a fresh sandbox.
	Snapshot(ctx context.Context, sandboxID ID) (SnapshotID, error)
	Restore(ctx context.Context, sandboxID ID, id SnapshotID) error

	// RemoveStorage deletes the storage a driver keeps for a session across recreates (e.g. a VM's home
	// disk), named by SandboxSpec.SessionID. Destroy leaves it by design, so the session layer calls this
	// when the session itself goes. Removing storage that does not exist is not an error.
	RemoveStorage(ctx context.Context, sessionID string) error
}

// SandboxSpec is the create-time configuration for a sandbox. Drivers translate it into their own
// native representation.
type SandboxSpec struct {
	// SessionID is the caller's stable identity for this sandbox, unchanged when the sandbox itself is
	// recreated. Drivers use it to name storage that must outlive a single sandbox, such as the VM
	// runtime's per session home disk. Empty means the driver keeps no such storage.
	SessionID string

	// Image is the canonical OCI image reference. A VM driver flattens it into a read only root
	// filesystem at create time.
	Image string

	// Cmd is the entrypoint a guest runs: it sets the agent up and writes its launch script. A runtime with
	// no guest does its own setup and only checks that Cmd is there.
	Cmd []string

	// Agent is the coding agent this sandbox hosts.
	Agent Agent

	// SharedLoginAgents are the agents whose shared login this sandbox may be handed: its own agent on a
	// session's machine, every agent on the machine sessions share.
	SharedLoginAgents []string

	// Mounts are host paths shared with the sandbox. Source paths must be owned by this node;
	// sessions never cross host boundaries. ValidateSpec enforces this.
	Mounts []Mount

	// Env is the process environment seen by the sandbox's entry
	// process. ExecSpec.Env layers on top for individual Exec calls.
	Env map[string]string

	// Resources caps the sandbox. Zero values mean "no explicit
	// limit"; drivers pick a sensible default or ignore them.
	Resources Resources
}

// Mount is a single host→sandbox path mapping.
type Mount struct {
	Source   string // host path (must be absolute, cleaned, and under an allowed root)
	Target   string // path inside the sandbox (must be absolute)
	ReadOnly bool
}

// Resources limits CPU, memory, and disk for a sandbox.
type Resources struct {
	CPU    float64 // CPU cores; a VM rounds up to whole cores
	Memory int64   // bytes
	Disk   int64   // bytes; sizes a VM's writable layer
}

// ExecSpec is the per-call config for Exec.
type ExecSpec struct {
	Cmd        []string          // argv; Cmd[0] is the program
	Env        map[string]string // layered onto SandboxSpec.Env
	WorkingDir string            // empty = sandbox default
	Tty        bool              // allocate a TTY (needed for tmux attach)
	Stdin      io.Reader         // nil = no stdin
}

// Process is a handle to a running command inside a sandbox. It owns underlying resources (typically a
// vsock connection into the guest, or a local process); the caller MUST either Wait it to completion
// (which closes on return) or call Close to abandon it. Forgetting both leaks goroutines and file
// descriptors.
type Process interface {
	io.Closer

	// Stdout / Stderr are the running command's output streams. They are closed when the process exits
	// or Close is called, whichever happens first.
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser

	// Stdin returns a writer that forwards into the process's stdin. Only meaningful if ExecSpec.Stdin
	// was nil at create time AND ExecSpec.Tty was true; interactive callers need to keep writing input
	// after the exec started. Drivers that can't expose a writer return ErrNotSupported.
	Stdin() io.WriteCloser

	// Wait blocks until the process exits, ctx is cancelled, or
	// Close is called. exitCode is the OS exit status; err is
	// non-nil only when the outcome couldn't be determined.
	// Successful Wait implicitly Close()s the process.
	Wait(ctx context.Context) (exitCode int, err error)

	// Signal forwards a Unix signal into the sandbox. Drivers that
	// can't deliver per-exec signals return ErrNotSupported.
	Signal(s os.Signal) error

	// Resize tells a TTY-allocated exec that the controlling
	// terminal has new dimensions. Only meaningful when
	// ExecSpec.Tty was true. Drivers that don't support resize
	// return ErrNotSupported.
	Resize(cols, rows uint16) error
}

// LogOpts controls Sandbox.Logs.
type LogOpts struct {
	Follow bool      // stay open across new writes
	Tail   int       // last N lines; 0 = all
	Since  time.Time // zero value = beginning of stream
}

// SnapshotID names a stored snapshot. The format is driver-defined.
type SnapshotID string

// ShareRoots says which host folders a node will share into a sandbox. A shared folder must sit in one
// of Allowed, and may not equal, sit inside or contain any of Refused. Empty Allowed shares nothing.
type ShareRoots struct {
	Allowed []string
	Refused []string
}

// ValidateSpec checks a spec before a driver builds anything from it: it needs an image, and every mount
// source must be absolute, cleaned, and allowed by roots. Drivers that share host folders call it, so the
// same policy holds regardless of backend; sandboxless takes no image and no mounts, so it does not.
func ValidateSpec(s SandboxSpec, roots ShareRoots) error {
	if s.Image == "" {
		return fmt.Errorf("sandbox: spec.Image is required")
	}
	for i, m := range s.Mounts {
		if err := validateMount(m, roots); err != nil {
			return fmt.Errorf("sandbox: spec.Mounts[%d]: %w", i, err)
		}
	}
	return nil
}

// MountSourceAllowed reports whether a host folder may be shared into a sandbox. Exported so the
// session layer can refuse one up front, by the same rule the drivers enforce at create time.
func MountSourceAllowed(source string, roots ShareRoots) error {
	return validateMount(Mount{Source: source, Target: "/mnt"}, roots)
}

func validateMount(m Mount, roots ShareRoots) error {
	if m.Source == "" {
		return fmt.Errorf("mount source is empty")
	}
	if !filepath.IsAbs(m.Target) {
		return fmt.Errorf("mount target %q is not absolute", m.Target)
	}
	if !filepath.IsAbs(m.Source) {
		return fmt.Errorf("mount source %q is not absolute", m.Source)
	}
	cleaned := filepath.Clean(m.Source)
	if cleaned != m.Source {
		return fmt.Errorf("mount source %q is not in cleaned form (want %q)", m.Source, cleaned)
	}
	source := ResolveSymlinks(cleaned)
	for _, refused := range roots.Refused {
		folder := ResolveSymlinks(filepath.Clean(refused))
		if isSameFolderOrInside(source, folder) || isSameFolderOrInside(folder, source) {
			return fmt.Errorf("%s can't be shared: %s holds deepcove's own files, and a session could change them",
				m.Source, refused)
		}
	}
	for _, root := range roots.Allowed {
		if isSameFolderOrInside(source, ResolveSymlinks(filepath.Clean(root))) {
			return nil
		}
	}
	allowed := strings.Join(roots.Allowed, ", ")
	// Inside as typed, outside once its links are followed: say it was a link, or the refusal reads wrong.
	for _, root := range roots.Allowed {
		if isSameFolderOrInside(cleaned, filepath.Clean(root)) {
			return fmt.Errorf("%s can't be shared: it is a link to %s, which is outside %s. Only folders inside %s can be shared",
				m.Source, source, allowed, allowed)
		}
	}
	return fmt.Errorf("%s can't be shared: only folders inside %s can", m.Source, allowed)
}

// isSameFolderOrInside reports whether path is folder or inside it. The spellings are compared first,
// ignoring case on macOS, whose disk ignores it too, so this holds even for a folder not created yet.
// Where both exist, the folders themselves are compared, which catches any other spelling of the same one.
func isSameFolderOrInside(path, folder string) bool {
	spelledPath, spelledFolder := path, folder
	if runtime.GOOS == "darwin" {
		spelledPath, spelledFolder = strings.ToLower(path), strings.ToLower(folder)
	}
	if spelledPath == spelledFolder || strings.HasPrefix(spelledPath, spelledFolder+string(filepath.Separator)) {
		return true
	}
	folderInfo, err := os.Stat(folder)
	if err != nil {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(info, folderInfo) {
			return true
		}
		if current == filepath.Dir(current) {
			return false
		}
	}
}

// ResolveSymlinks follows every link in path, so a link inside an allowed root cannot point a share
// somewhere else. The part that does not exist yet is kept as written, since a folder can be created
// after it is checked.
func ResolveSymlinks(path string) string {
	rest := ""
	for current := path; ; current = filepath.Dir(current) {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(real, rest)
		}
		if current == filepath.Dir(current) {
			return path
		}
		rest = filepath.Join(filepath.Base(current), rest)
	}
}
