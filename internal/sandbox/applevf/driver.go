package applevf

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Driver implements sandbox.Sandbox by running each session in its own Apple Virtualization.framework
// VM, the isolation counterpart to the Docker driver's shared kernel containers.
//
// Session lifecycle maps onto the VM lifecycle: Create resolves and builds the read only base image
// (cached per digest, so only the first session for an image pays for it), Start boots the VM, Stop asks
// the guest to power off over the control channel, and Destroy releases everything.
type Driver struct {
	images    *imageStore
	kernel    string
	guestInit string
	creds     credbroker.Store

	mu       sync.Mutex
	machines map[sandbox.Handle]*session
}

// session is one created sandbox: its spec, its resolved base image, and the VM once started.
type session struct {
	spec      sandbox.SandboxSpec
	baseImage string
	machine   *machine
}

// Option configures a Driver.
type Option func(*Driver)

// WithCredentialStore enables the credential broker, which lets sessions of the same agent share one
// login without sharing a disk. Without it a session starts with no stored credentials.
func WithCredentialStore(store credbroker.Store) Option {
	return func(d *Driver) { d.creds = store }
}

// New builds a Driver. kernelPath is the guest kernel (a Kata style build with virtio and overlayfs
// compiled in), guestInitPath is the compiled Linux init binary baked into every image, and imageDir is
// where built base images are cached.
func New(kernelPath, guestInitPath, imageDir string, opts ...Option) (*Driver, error) {
	if kernelPath == "" {
		return nil, fmt.Errorf("applevf: a guest kernel path is required")
	}
	if guestInitPath == "" {
		return nil, fmt.Errorf("applevf: a guest init binary path is required")
	}
	d := &Driver{
		images:    newImageStore(imageDir),
		kernel:    kernelPath,
		guestInit: guestInitPath,
		machines:  map[sandbox.Handle]*session{},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

var (
	_ sandbox.Sandbox      = (*Driver)(nil)
	_ sandbox.Pinger       = (*Driver)(nil)
	_ sandbox.ImageManager = (*Driver)(nil)
)

// Create resolves the session's image into a cached read only ext4 base and records the spec. The VM is
// not booted until Start, mirroring the Docker driver's create then start split.
func (d *Driver) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.Handle, error) {
	if err := sandbox.ValidateSpec(spec, nil); err != nil {
		return "", err
	}
	base, err := d.images.baseImage(ctx, spec.Image)
	if err != nil {
		return "", err
	}
	handle := sandbox.Handle(newHandleID())

	d.mu.Lock()
	defer d.mu.Unlock()
	d.machines[handle] = &session{spec: spec, baseImage: base}
	return handle, nil
}

// Start boots the VM for a created sandbox and, when a credential store is configured, begins serving the
// guest's credential requests.
func (d *Driver) Start(_ context.Context, h sandbox.Handle) error {
	s, err := d.lookup(h)
	if err != nil {
		return err
	}
	if s.machine != nil {
		return fmt.Errorf("applevf: sandbox %s is already started: %w", h, sandbox.ErrConflict)
	}

	m, err := bootVM(vmSpec{
		KernelPath:  d.kernel,
		Cmdline:     defaultCmdline().String(),
		BaseImage:   s.baseImage,
		CPUCount:    cpuCount(s.spec.Resources),
		MemoryBytes: memoryBytes(s.spec.Resources),
	})
	if err != nil {
		return err
	}
	if d.creds != nil {
		if err := serveGuestCredentials(m, d.creds); err != nil {
			_ = m.Stop()
			return err
		}
	}

	d.mu.Lock()
	s.machine = m
	d.mu.Unlock()
	return nil
}

// Stop asks the guest to power itself off, which also gives it the chance to hand back refreshed
// credentials. Stopping an already stopped sandbox is not an error.
func (d *Driver) Stop(_ context.Context, h sandbox.Handle) error {
	s, err := d.lookup(h)
	if err != nil {
		return err
	}
	d.mu.Lock()
	m := s.machine
	s.machine = nil
	d.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.Stop()
}

// Destroy stops the VM if it is running and forgets the sandbox. The cached base image survives, because
// it is shared by every session using that image.
func (d *Driver) Destroy(ctx context.Context, h sandbox.Handle) error {
	if err := d.Stop(ctx, h); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.machines, h)
	return nil
}

// Exec runs a command inside a started VM over vsock.
func (d *Driver) Exec(_ context.Context, h sandbox.Handle, cmd sandbox.ExecSpec) (sandbox.Process, error) {
	m, err := d.runningMachine(h)
	if err != nil {
		return nil, err
	}
	return m.Exec(guestexec.ExecRequest{
		Argv: cmd.Cmd,
		Env:  cmd.Env,
		Dir:  cmd.WorkingDir,
		Tty:  cmd.Tty,
	})
}

// Logs returns the guest's serial console, which carries kernel messages and the init's boot progress.
// It is a live stream rather than a replayable buffer, so LogOpts is ignored.
func (d *Driver) Logs(_ context.Context, h sandbox.Handle, _ sandbox.LogOpts) (io.ReadCloser, error) {
	m, err := d.runningMachine(h)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(m.Console()), nil
}

// Ping reports whether this host can run VMs at all, which is what the preflight check surfaces.
func (d *Driver) Ping(context.Context) (string, error) { return platformVersion() }

// ImageExists reports whether a base image is already built and cached for ref.
func (d *Driver) ImageExists(ctx context.Context, ref string) (bool, error) {
	return d.images.cached(ctx, ref)
}

// ImagePull builds the cached base image for ref, which subsumes pulling it. Progress is reported once
// as indeterminate, since the work is a pull followed by a filesystem build rather than layer downloads
// the registry client exposes incrementally.
func (d *Driver) ImagePull(ctx context.Context, ref string, onProgress func(sandbox.PullProgress)) error {
	if onProgress != nil {
		onProgress(sandbox.PullProgress{Status: "Building VM image"})
	}
	_, err := d.images.baseImage(ctx, ref)
	return err
}

// CopyIn and CopyOut are not implemented yet. The session layer does not use them, and moving files into
// a VM needs a guest side helper rather than a daemon API call.
func (d *Driver) CopyIn(context.Context, sandbox.Handle, string, string) error {
	return fmt.Errorf("applevf: copy into a VM is not implemented: %w", sandbox.ErrNotSupported)
}

func (d *Driver) CopyOut(context.Context, sandbox.Handle, string, string) error {
	return fmt.Errorf("applevf: copy out of a VM is not implemented: %w", sandbox.ErrNotSupported)
}

// Snapshot and Restore are optional; the session layer falls back to cold starting a fresh VM.
func (d *Driver) Snapshot(context.Context, sandbox.Handle) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (d *Driver) Restore(context.Context, sandbox.Handle, sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// RemoveStorage has nothing to remove: this runtime has no named volumes. Agent state that must outlive
// a session goes through the credential broker instead.
func (d *Driver) RemoveStorage(context.Context, string) error {
	return sandbox.ErrNotSupported
}

// Close stops every running VM. Used when the process shuts down.
func (d *Driver) Close() error {
	d.mu.Lock()
	running := make([]*machine, 0, len(d.machines))
	for _, s := range d.machines {
		if s.machine != nil {
			running = append(running, s.machine)
			s.machine = nil
		}
	}
	d.mu.Unlock()

	var firstErr error
	for _, m := range running {
		if err := m.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (d *Driver) lookup(h sandbox.Handle) (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.machines[h]
	if !ok {
		return nil, fmt.Errorf("applevf: unknown sandbox %s: %w", h, sandbox.ErrNotFound)
	}
	return s, nil
}

func (d *Driver) runningMachine(h sandbox.Handle) (*machine, error) {
	s, err := d.lookup(h)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.machine == nil {
		return nil, fmt.Errorf("applevf: sandbox %s is not running: %w", h, sandbox.ErrConflict)
	}
	return s.machine, nil
}

// Resource defaults. A VM must be given concrete values, unlike a container that can inherit the host's,
// so an unset limit becomes a sensible default rather than zero.
const (
	defaultCPUCount    uint   = 2
	defaultMemoryBytes uint64 = 2 << 30 // 2 GiB
)

func cpuCount(r sandbox.Resources) uint {
	if r.CPU <= 0 {
		return defaultCPUCount
	}
	return uint(r.CPU)
}

func memoryBytes(r sandbox.Resources) uint64 {
	if r.Memory <= 0 {
		return defaultMemoryBytes
	}
	return uint64(r.Memory)
}
