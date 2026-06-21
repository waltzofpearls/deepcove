// Package sandbox abstracts the runtime layer that hosts coding-agent sessions. Drivers live in
// sibling subpackages (e.g. sandbox/docker) so the heavy SDK dependencies of each backend don't leak
// into callers that only need the interface, the same pattern as database/sql vs. database/sql/driver.
//
// Design highlights:
//   - Every session runs inside a sandbox. There is no "no-sandbox" fast path.
//   - The interface accommodates per-session microVMs (Firecracker, Apple Virtualization.framework)
//     as well as containers (Docker). Snapshot/Restore are optional and return ErrNotSupported on
//     backends that lack them.
//   - SandboxSpec.Mounts only ever points at host paths this node owns. Sessions never cross host
//     boundaries. ValidateSpec enforces this for every driver.
//   - Tailscale membership (tsnet) is the node's, not the sandbox's. Network here is just the
//     sandbox's own netns / port mapping.
//
// v1 ships with the Docker driver only. The interface is locked down from day one so the session
// layer doesn't grow Docker-specific assumptions that would have to be unwound when a second driver
// lands.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sentinel errors. Drivers normalize their native error types into
// these so the session layer can react portably:
//
//   - ErrNotSupported: the operation isn't implemented by this driver
//     (e.g. Snapshot on Docker). Treat as a perf-only signal.
//   - ErrNotFound: the handle doesn't refer to a live sandbox (e.g.
//     someone destroyed it out of band). Stop/Destroy can treat this
//     as idempotent success.
//   - ErrConflict: the operation can't be performed in the current
//     state (e.g. Start on an already-running sandbox).
var (
	ErrNotSupported = errors.New("sandbox: operation not supported by this driver")
	ErrNotFound     = errors.New("sandbox: handle not found")
	ErrConflict     = errors.New("sandbox: lifecycle conflict")

	// ErrUnavailable means the backend itself is unreachable: the Docker daemon isn't running, the
	// socket is gone, etc. Distinct from ErrNotFound (the daemon is up but the handle/image is
	// absent). The session layer maps this to a 503 + actionable "start Docker" guidance instead of
	// a raw socket error.
	ErrUnavailable = errors.New("sandbox: backend unavailable")

	// ErrImageNotFound means the requested image isn't present on the
	// backend and hasn't been pulled. The session layer maps this to a
	// 422 so the UI can offer to download it, rather than surfacing the
	// daemon's raw "No such image" string.
	ErrImageNotFound = errors.New("sandbox: image not found")
)

// Pinger is an optional capability: a driver that can cheaply check
// whether its backend is reachable implements it. The session layer
// uses it for the GET /api/preflight environment check. Drivers that
// don't implement it are treated as "readiness unknown."
type Pinger interface {
	// Ping reports backend liveness. The returned string is a
	// human-meaningful version identifier (e.g. the Docker API
	// version) when reachable; it's best-effort and may be empty.
	// A non-nil error (typically wrapping ErrUnavailable) means the
	// backend is unreachable.
	Ping(ctx context.Context) (version string, err error)
}

// ImageManager is an optional capability for container-style backends
// that pull images from a registry. microVM backends that materialize
// a rootfs differently won't implement it, and the session layer skips
// the image-readiness path for them.
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

// Handle is an opaque, driver-owned reference to a created sandbox. Treat it as a token to pass back
// into the same driver; do not interpret its contents. Drivers may store side state keyed off the
// handle internally.
type Handle string

// Sandbox is the surface every driver implements. The minimal set covering Docker, Firecracker, and
// Apple Virtualization.framework.
type Sandbox interface {
	// Lifecycle.
	Create(ctx context.Context, spec SandboxSpec) (Handle, error)
	Start(ctx context.Context, h Handle) error
	Stop(ctx context.Context, h Handle) error
	Destroy(ctx context.Context, h Handle) error

	// Exec runs a command inside an already-Started sandbox. The returned Process is owned by the
	// caller; call Close on it (or Wait, which closes on completion) to release the underlying
	// connection.
	Exec(ctx context.Context, h Handle, cmd ExecSpec) (Process, error)

	// Logs returns the sandbox's stdout/stderr stream. Follow=true
	// keeps the reader open across new writes; closing the reader
	// is the caller's responsibility.
	Logs(ctx context.Context, h Handle, opts LogOpts) (io.ReadCloser, error)

	// CopyIn / CopyOut move single paths between host and sandbox.
	// Behavior on directories is driver-defined (most copy recursively).
	CopyIn(ctx context.Context, h Handle, src, dst string) error
	CopyOut(ctx context.Context, h Handle, src, dst string) error

	// Snapshot / Restore are optional. Drivers without support
	// return ErrNotSupported and the session layer falls back to
	// cold-creating a fresh sandbox.
	Snapshot(ctx context.Context, h Handle) (SnapshotID, error)
	Restore(ctx context.Context, h Handle, id SnapshotID) error

	// RemoveStorage deletes a named persistent storage primitive (e.g. a Docker volume, a microVM
	// volume image) by name. Used by the session layer to clean up the agent-home volume when a
	// session is destroyed; named volumes survive container removal by design, so destroy needs a
	// separate step to release them. Drivers without named storage return ErrNotSupported; the
	// session layer treats that as a no-op. ErrNotFound is returned if the name doesn't exist.
	RemoveStorage(ctx context.Context, name string) error
}

// SandboxSpec is the create-time configuration for a sandbox. Drivers
// translate this into their own native representation (container
// config, microVM JSON, etc).
type SandboxSpec struct {
	// Image is the canonical OCI image reference. Container drivers
	// pull it; microVM drivers materialize it to a rootfs at create
	// time (e.g. via skopeo + umoci or firecracker-containerd).
	Image string

	// Cmd overrides the image's default command (Dockerfile CMD or
	// equivalent). Nil/empty = use the image default.
	Cmd []string

	// Mounts are host paths shared with the sandbox. Source paths must be owned by this node;
	// sessions never cross host boundaries. ValidateSpec enforces this.
	Mounts []Mount

	// Env is the process environment seen by the sandbox's entry
	// process. ExecSpec.Env layers on top for individual Exec calls.
	Env map[string]string

	// Resources caps the sandbox. Zero values mean "no explicit
	// limit"; drivers pick a sensible default or ignore (e.g. the
	// Docker driver ignores Resources.Disk because Docker disk quotas
	// aren't portable across storage drivers).
	Resources Resources

	// Network configures the sandbox's networking. tsnet membership is not represented here; it lives
	// on the node, and proxies reach into the sandbox via Network.Ports.
	Network NetworkConfig
}

// Mount is a single host→sandbox path mapping.
type Mount struct {
	Source   string    // host path (must be absolute, cleaned, and under an allowed root)
	Target   string    // path inside the sandbox (must be absolute)
	ReadOnly bool
	Type     MountType // Bind = live host share; CopyIn = snapshot at create
}

// MountType selects the sharing primitive used for a mount. Drivers
// pick the appropriate native mechanism (bind mount for containers,
// virtio-fs or 9p for microVMs, or a copy-in for snapshot-style).
type MountType int

const (
	// MountBind shares the host path live with the sandbox. Writes
	// inside the sandbox are visible on the host and vice versa.
	// Containers implement this as a bind mount; microVMs as virtio-fs
	// or 9p.
	MountBind MountType = iota

	// MountCopyIn copies the contents of Source into the sandbox at
	// create time and does not propagate changes back. Useful for
	// templates or read-only seed data on microVM backends that
	// can't (or don't want to) share live.
	MountCopyIn

	// MountVolume is a driver-managed persistent storage primitive. For Docker: a named volume
	// (`docker volume`). For microVM backends: a per-volume filesystem image. Survives container
	// recreate by design; used by the session layer to persist agent state (`/root` and friends)
	// across the destroy+recreate cycle of an edit-mounts operation. Source is the volume name, not a
	// host path, so the allowed-roots check is skipped in validateMount.
	MountVolume
)

// Resources limits CPU, memory, and disk for a sandbox.
type Resources struct {
	CPU    float64 // CPU cores; fractional ok (e.g. 1.5)
	Memory int64   // bytes
	Disk   int64   // bytes; Docker driver ignores this
}

// NetworkConfig describes the sandbox's own networking. The
// node-level tailnet is layered on top of this by the runtime.
type NetworkConfig struct {
	// Mode picks the backend's networking primitive. Drivers map
	// this onto their native concept (Docker network-mode, microVM
	// netdev, etc).
	Mode NetworkMode

	// Ports are host:sandbox forwards. Microvm drivers may
	// implement these via vsock, container drivers via iptables.
	Ports []PortMapping
}

// NetworkMode selects the sandbox's networking primitive. The zero value is NetIsolated: a sandbox
// with no Network field set has no network access, which is the safe default for an agent that hasn't
// been configured to need it.
type NetworkMode int

const (
	// NetIsolated gives the sandbox no network. Useful for batch
	// jobs that only read host-mounted inputs.
	NetIsolated NetworkMode = iota

	// NetNAT gives the sandbox outbound network access via a NAT
	// (Docker bridge, microVM tap, etc) and supports Ports.
	NetNAT

	// NetHost shares the host's network namespace with the sandbox.
	// Maximum connectivity, minimum isolation; not all drivers
	// support it (e.g. microVMs cannot).
	NetHost
)

// PortMapping forwards a host port to a sandbox port.
type PortMapping struct {
	Host     int
	Sandbox  int
	Protocol string // "tcp" or "udp"; empty defaults to "tcp"
}

// ExecSpec is the per-call config for Exec.
type ExecSpec struct {
	Cmd        []string          // argv; Cmd[0] is the program
	Env        map[string]string // layered onto SandboxSpec.Env
	WorkingDir string            // empty = sandbox default
	Tty        bool              // allocate a TTY (needed for tmux attach)
	Stdin      io.Reader         // nil = no stdin
}

// Process is a handle to a running command inside a sandbox. It owns underlying resources (typically
// a hijacked connection to the runtime daemon); the caller MUST either Wait it to completion (which
// closes on return) or call Close to abandon it. Forgetting both leaks goroutines and file
// descriptors.
type Process interface {
	io.Closer

	// Stdout / Stderr are demultiplexed streams from the running
	// command. They are closed when the process exits or Close is
	// called, whichever happens first.
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

// ValidateSpec applies the cross-driver mount invariants: mount sources must be absolute, cleaned,
// and confined under one of allowedRoots. Drivers should call this before invoking their daemon so
// the same policy is enforced regardless of backend.
//
// allowedRoots is the list of host directories the node has decided it is willing to share with
// sandboxes (typically just the data directory's sessions subtree). An empty allowedRoots means no
// mounts are allowed at all.
func ValidateSpec(s SandboxSpec, allowedRoots []string) error {
	if s.Image == "" {
		return fmt.Errorf("sandbox: spec.Image is required")
	}
	for i, m := range s.Mounts {
		if err := validateMount(m, allowedRoots); err != nil {
			return fmt.Errorf("sandbox: spec.Mounts[%d]: %w", i, err)
		}
	}
	return nil
}

func validateMount(m Mount, allowedRoots []string) error {
	if m.Source == "" {
		return fmt.Errorf("mount source is empty")
	}
	if !filepath.IsAbs(m.Target) {
		return fmt.Errorf("mount target %q is not absolute", m.Target)
	}
	// Volume mounts use Source as a driver-managed volume name, not a host path. They don't go through
	// the allowed-roots gate because the storage lives outside the host filesystem from the user's POV
	// (Docker volumes are stored under the daemon's data dir, not $HOME). Only the host-path checks
	// below are skipped; empty and non-absolute target still get caught above.
	if m.Type == MountVolume {
		return nil
	}
	if !filepath.IsAbs(m.Source) {
		return fmt.Errorf("mount source %q is not absolute", m.Source)
	}
	cleaned := filepath.Clean(m.Source)
	if cleaned != m.Source {
		return fmt.Errorf("mount source %q is not in cleaned form (want %q)", m.Source, cleaned)
	}
	for _, root := range allowedRoots {
		root = filepath.Clean(root)
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("mount source %q is outside the allowed roots", m.Source)
}
