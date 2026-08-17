package applevf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestcfg"
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
	disks     string
	kernel    string
	guestInit string
	creds     credbroker.Store
	logger    *slog.Logger
	// allowedRoots bounds which host directories may be shared into a VM, the same gate the container
	// runtime applies. Empty means no host path may be mounted.
	allowedRoots []string

	mu       sync.Mutex
	machines map[sandbox.ID]*session
}

// session is one created sandbox: its spec, its resolved base image, and the VM once started.
type session struct {
	spec      sandbox.SandboxSpec
	baseImage string
	machine   *machine
	// starting is set while Start is booting this VM. Guarded by the driver's mutex, like machine.
	starting  bool
	createdAt time.Time
}

// Option configures a Driver.
type Option func(*Driver)

// WithLogger records VM lifecycle transitions. Worth having because these VMs run IN PROCESS, as threads
// inside deepcove rather than as OS processes, so nothing external (ps, Activity Monitor, a docker ps
// equivalent) can ever show them. The log is the only way to watch a VM come and go until the VM
// management view lands.
func WithLogger(logger *slog.Logger) Option {
	return func(d *Driver) { d.logger = logger }
}

// WithAllowedMountRoots sets the host directories whose contents may be shared into a VM. Mount sources
// outside these roots are rejected at Create.
func WithAllowedMountRoots(roots ...string) Option {
	return func(d *Driver) { d.allowedRoots = roots }
}

// WithCredentialStore enables the credential broker, which lets sessions of the same agent share one
// login without sharing a disk. Without it a session starts with no stored credentials.
func WithCredentialStore(store credbroker.Store) Option {
	return func(d *Driver) { d.creds = store }
}

// New builds a Driver. kernelPath is the guest kernel (a Kata style build with virtio and overlayfs
// compiled in), guestInitPath is the compiled Linux init binary baked into every image, and stateDir is
// where this runtime keeps what it owns: cached base images, and the per session home disks.
func New(kernelPath, guestInitPath, stateDir string, opts ...Option) (*Driver, error) {
	if kernelPath == "" {
		return nil, fmt.Errorf("applevf: a guest kernel path is required")
	}
	if guestInitPath == "" {
		return nil, fmt.Errorf("applevf: a guest init binary path is required")
	}
	images, err := newImageStore(filepath.Join(stateDir, "vm-images"), baseInjection(guestInitPath))
	if err != nil {
		return nil, fmt.Errorf("applevf: %w", err)
	}
	d := &Driver{
		// Only the constant parts go in the shared, digest cached base image. The per session entrypoint
		// is served over vsock at boot instead, so sessions using the same image still share one build.
		images:    images,
		disks:     filepath.Join(stateDir, "vm-disks"),
		kernel:    kernelPath,
		guestInit: guestInitPath,
		machines:  map[sandbox.ID]*session{},
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.logger == nil {
		d.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	d.sweepSwapDisks()
	return d, nil
}

// sweepSwapDisks removes swap disks left by a previous process.
//
// A VM dies with the process that ran it, so anything here at startup belongs to a sandbox that no
// longer exists, and swap is worthless across a boot regardless. Destroy removes the swap of a machine
// it tears down; this covers the machines it never got to, which is all of them after a restart.
// Without it they accumulate one per sandbox, forever.
func (d *Driver) sweepSwapDisks() {
	matches, err := filepath.Glob(filepath.Join(d.disks, "swap-*.img"))
	if err != nil || len(matches) == 0 {
		return
	}
	removed := 0
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			d.logger.Warn("could not remove a stale swap disk", "path", path, "err", err)
			continue
		}
		removed++
	}
	d.logger.Info("removed stale swap disks", "count", removed)
}

var (
	_ sandbox.Sandbox      = (*Driver)(nil)
	_ sandbox.Pinger       = (*Driver)(nil)
	_ sandbox.ImageManager = (*Driver)(nil)
)

// Create resolves the session's image into a cached read only ext4 base and records the spec. The VM is
// not booted until Start, mirroring the Docker driver's create then start split.
func (d *Driver) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.ID, error) {
	if err := sandbox.ValidateSpec(spec, d.allowedRoots); err != nil {
		return "", err
	}
	if len(spec.Cmd) == 0 {
		return "", fmt.Errorf("applevf: spec.Cmd is required; it is the session entrypoint the guest runs")
	}
	base, err := d.images.baseImage(ctx, spec.Image)
	if err != nil {
		return "", err
	}
	handle := sandbox.ID(newHandleID())

	d.mu.Lock()
	defer d.mu.Unlock()
	d.machines[handle] = &session{spec: spec, baseImage: base, createdAt: time.Now()}
	return handle, nil
}

// Start boots the VM for a created sandbox and, when a credential store is configured, begins serving the
// guest's credential requests.
func (d *Driver) Start(ctx context.Context, h sandbox.ID) error {
	s, err := d.lookup(h)
	if err != nil {
		return err
	}
	if s.machine != nil {
		return fmt.Errorf("applevf: sandbox %s is already started: %w", h, sandbox.ErrConflict)
	}
	d.mu.Lock()
	s.starting = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		s.starting = false
		d.mu.Unlock()
	}()

	shares, guestMounts := hostShares(s.spec.Mounts)
	home, err := d.homeDisk(s.spec)
	if err != nil {
		return err
	}
	memory := memoryBytes(s.spec.Resources)
	swap, err := d.swapDisk(h, memory)
	if err != nil {
		return err
	}
	m, err := bootVM(vmSpec{
		KernelPath:   d.kernel,
		Cmdline:      defaultCmdline().String(),
		BaseImage:    s.baseImage,
		ScratchImage: home,
		SwapImage:    swap,
		CPUCount:     cpuCount(s.spec.Resources),
		MemoryBytes:  memory,
		Shares:       shares,
	})
	if err != nil {
		return err
	}
	homeDevice, swapDevice := guestDevices(home, swap)
	// The guest asks for both of these at boot, so they have to be listening before it gets that far.
	if err := serveGuestConfig(m, guestcfg.Config{
		Entrypoint:   s.spec.Cmd,
		Agent:        s.spec.Agent,
		Env:          s.spec.Env,
		OverlayBytes: s.spec.Resources.Disk,
		Mounts:       guestMounts,
		HomeDevice:   homeDevice,
		HomeTarget:   homeMountTarget(s.spec.Mounts),
		SwapDevice:   swapDevice,
	}); err != nil {
		_ = m.Stop()
		return err
	}
	if d.creds != nil {
		if err := serveGuestCredentials(m, d.creds); err != nil {
			_ = m.Stop()
			return err
		}
	}

	// Drain the console before waiting: it is an OS pipe, so an undrained one eventually blocks the
	// guest mid boot, and its contents are what explain a failure to come up.
	m.captureConsole()

	// Do not report a running session until the guest actually answers. Otherwise a guest that panicked
	// still looks healthy and only reveals itself later as an empty terminal.
	if err := m.waitReady(ctx, bootTimeout); err != nil {
		_ = m.Stop()
		return err
	}

	d.mu.Lock()
	s.machine = m
	live := d.liveCountLocked()
	d.mu.Unlock()
	d.logger.Info("vm started", "handle", h, "image", s.spec.Image, "live_vms", live)
	return nil
}

// ListSandboxes reports every VM this driver tracks. The registry is in memory and dies with the
// process, so after a restart this is legitimately empty while the session table still names handles.
// That is not a bug to paper over: those sessions really have lost their VMs, and the view should say so.
func (d *Driver) ListSandboxes(context.Context) ([]sandbox.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]sandbox.Instance, 0, len(d.machines))
	for h, s := range d.machines {
		state := sandbox.InstanceStopped
		switch {
		case s.machine != nil:
			state = sandbox.InstanceRunning
		case s.starting:
			state = sandbox.InstanceStarting
		}
		out = append(out, sandbox.Instance{
			ID:        h,
			State:     state,
			Image:     s.spec.Image,
			Mounts:    hostMounts(s.spec.Mounts),
			Resources: actualResources(s.spec.Resources),
			CreatedAt: s.createdAt,
		})
	}
	// Map iteration order is random, so sort for a view that does not reshuffle on every poll. Newest
	// first matches how sessions are listed, and puts what you just created at the top.
	slices.SortFunc(out, func(a, b sandbox.Instance) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return out, nil
}

// DefaultResources implements sandbox.ResourceDefaulter: what a session gets when it names no size.
func (d *Driver) DefaultResources() sandbox.Resources { return actualResources(sandbox.Resources{}) }

// hostMounts keeps the shares that name a real host folder. A volume mount's source is a driver managed
// name, which a VM has no equivalent for and a reader cannot act on.
func hostMounts(mounts []sandbox.Mount) []sandbox.Mount {
	out := make([]sandbox.Mount, 0, len(mounts))
	for _, m := range mounts {
		if m.Type != sandbox.MountVolume {
			out = append(out, m)
		}
	}
	return out
}

// hostShares turns the spec's mounts into virtio-fs shares and the matching guest side mount list.
//
// Volume mounts are skipped. Their Source names a driver managed volume rather than a host path, and a VM
// has no equivalent: the agent's home comes from the credential broker instead.
func hostShares(mounts []sandbox.Mount) ([]vmShare, []guestcfg.Mount) {
	var shares []vmShare
	var guest []guestcfg.Mount
	for _, m := range mounts {
		if !filepath.IsAbs(m.Source) {
			continue
		}
		tag := shareTag(len(shares))
		shares = append(shares, vmShare{Tag: tag, HostPath: m.Source, ReadOnly: m.ReadOnly})
		guest = append(guest, guestcfg.Mount{Tag: tag, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	return shares, guest
}

// shareTag names the nth share. Short and positional: virtio-fs tags are length limited, and the guest
// only has to match what the host announced.
func shareTag(i int) string { return "dcshare" + strconv.Itoa(i) }

// actualResources reports what the VM was really given rather than what was requested, since the driver
// substitutes its own defaults for zero and rounds CPU up to whole cores.
//
// Disk is derived rather than passed through. An uncapped writable layer is not unlimited: it is a tmpfs
// mounted with no size option, and the kernel defaults that to half the available RAM. Reporting zero
// would read as "no cap", when the real cap just moves with the memory.
func actualResources(r sandbox.Resources) sandbox.Resources {
	out := sandbox.Resources{
		CPU:    float64(cpuCount(r)),
		Memory: int64(memoryBytes(r)),
		Disk:   r.Disk,
	}
	if out.Disk <= 0 {
		out.Disk = out.Memory / 2
	}
	return out
}

// liveCountLocked reports how many VMs are running. Callers hold d.mu.
func (d *Driver) liveCountLocked() int {
	n := 0
	for _, s := range d.machines {
		if s.machine != nil {
			n++
		}
	}
	return n
}

// bootTimeout bounds how long a guest may take to come up.
//
// Readiness does NOT wait for the agent to be usable, only for the guest to answer, and the entrypoint
// runs in the background, so package installs do not count against this. A healthy boot answers in a few
// seconds; the rest is headroom for a slow machine. Keeping it tight matters because this is how long a
// user waits to be told a VM failed to start, and it has to fit inside the API server WriteTimeout
// (30s) or the connection dies before the error can be written and the user sees an empty reply.
const bootTimeout = 20 * time.Second

// Stop asks the guest to power itself off, which also gives it the chance to hand back refreshed
// credentials. Stopping an already stopped sandbox is not an error.
func (d *Driver) Stop(_ context.Context, h sandbox.ID) error {
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
	err = m.Stop()

	d.mu.Lock()
	live := d.liveCountLocked()
	d.mu.Unlock()
	d.logger.Info("vm stopped", "handle", h, "live_vms", live, "err", err)
	return err
}

// Destroy stops the VM if it is running and forgets the sandbox. The cached base image survives, because
// it is shared by every session using that image.
func (d *Driver) Destroy(ctx context.Context, h sandbox.ID) error {
	if err := d.Stop(ctx, h); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.machines, h)
	live := d.liveCountLocked()
	tracked := len(d.machines)
	d.mu.Unlock()
	// Swap belongs to the machine, so it goes with it. The home disk does not: it is keyed by session and
	// outlives the sandbox on purpose.
	if err := os.Remove(filepath.Join(d.disks, "swap-"+string(h)+".img")); err != nil && !os.IsNotExist(err) {
		d.logger.Warn("could not remove swap disk", "handle", h, "err", err)
	}
	d.logger.Info("vm destroyed", "handle", h, "live_vms", live, "tracked_sandboxes", tracked)
	return nil
}

// Exec runs a command inside a started VM over vsock.
func (d *Driver) Exec(ctx context.Context, h sandbox.ID, cmd sandbox.ExecSpec) (sandbox.Process, error) {
	m, err := d.runningMachine(h)
	if err != nil {
		return nil, err
	}
	proc, err := m.Exec(ctx, guestexec.ExecRequest{
		Argv: cmd.Cmd,
		Env:  cmd.Env,
		Dir:  cmd.WorkingDir,
		Tty:  cmd.Tty,
	})
	if err != nil {
		return nil, err
	}
	// A spec that carries its own stdin has to have it delivered AND closed. Dropping it silently is how
	// file upload broke on this runtime: the session layer runs `cat > file` and streams the bytes here,
	// so with nothing written and no EOF the command sat waiting for input that was never coming.
	if cmd.Stdin != nil {
		go streamStdin(proc, cmd.Stdin)
	}
	return proc, nil
}

// streamStdin feeds a caller supplied reader into a started process and closes stdin at EOF.
//
// The close is the part that matters: a command reading to EOF (`cat`, `tar -x`) never finishes without
// it, so the exec would report no status and the caller would block until its context expired. Errors
// are dropped because the process outliving its stdin is normal, and Wait already reports the outcome.
func streamStdin(proc sandbox.Process, r io.Reader) {
	w := proc.Stdin()
	if w == nil {
		return
	}
	_, _ = io.Copy(w, r)
	_ = w.Close()
}

// Logs returns the guest's serial console, which carries kernel messages and the init's boot progress.
// It is a live stream rather than a replayable buffer, so LogOpts is ignored.
func (d *Driver) Logs(_ context.Context, h sandbox.ID, _ sandbox.LogOpts) (io.ReadCloser, error) {
	m, err := d.runningMachine(h)
	if err != nil {
		return nil, err
	}
	if m.consoleLog == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	// The captured buffer, not the live console: the driver already drains that, and two readers would
	// each get a fraction of the output. LogOpts is ignored because this is a bounded tail, not a
	// replayable stream.
	return io.NopCloser(bytes.NewReader(m.consoleLog.Tail())), nil
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
func (d *Driver) CopyIn(context.Context, sandbox.ID, string, string) error {
	return fmt.Errorf("applevf: copy into a VM is not implemented: %w", sandbox.ErrNotSupported)
}

func (d *Driver) CopyOut(context.Context, sandbox.ID, string, string) error {
	return fmt.Errorf("applevf: copy out of a VM is not implemented: %w", sandbox.ErrNotSupported)
}

// Snapshot and Restore are optional; the session layer falls back to cold starting a fresh VM.
func (d *Driver) Snapshot(context.Context, sandbox.ID) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (d *Driver) Restore(context.Context, sandbox.ID, sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// homeDiskBytes is how big a session's home disk is. Sparse, so the file only occupies what the agent
// actually writes; this is the ceiling, not an allocation.
const homeDiskBytes = 16 << 30

// homeDisk returns the path of this session's home disk, creating it the first time. Raw and unformatted:
// the base image ships no mkfs, so the entrypoint formats it on first boot, where package installation
// is already happening.
//
// Named by session rather than by sandbox, so recreating the sandbox keeps the agent's home.
func (d *Driver) homeDisk(spec sandbox.SandboxSpec) (string, error) {
	if spec.SessionID == "" || homeMountTarget(spec.Mounts) == "" {
		return "", nil
	}
	if err := os.MkdirAll(d.disks, 0o700); err != nil {
		return "", fmt.Errorf("applevf: create disk dir: %w", err)
	}
	path := filepath.Join(d.disks, spec.SessionID+".img")
	switch _, err := os.Stat(path); {
	case err == nil:
		return path, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("applevf: stat home disk: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("applevf: create home disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(homeDiskBytes); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("applevf: size home disk: %w", err)
	}
	return path, nil
}

// swapDiskBytes is the ceiling for spill, not an allocation: the file is sparse, so it costs only what
// is actually swapped out. Twice the VM's memory, the traditional rule, which gives a workload that
// overflows RAM somewhere to go rather than being killed.
func swapDiskBytes(memory uint64) int64 {
	if memory == 0 {
		return 0
	}
	return int64(memory) * 2 //nolint:gosec // memory is a VM size, nowhere near overflowing
}

// swapDisk returns this sandbox's swap file, creating it the first time.
//
// Named by sandbox rather than by session, unlike the home disk: swap belongs to the machine and holds
// nothing worth keeping, so a rebuilt sandbox gets a fresh one.
func (d *Driver) swapDisk(h sandbox.ID, memory uint64) (string, error) {
	size := swapDiskBytes(memory)
	if size <= 0 {
		return "", nil
	}
	if err := os.MkdirAll(d.disks, 0o700); err != nil {
		return "", fmt.Errorf("applevf: create disk dir: %w", err)
	}
	path := filepath.Join(d.disks, "swap-"+string(h)+".img")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("applevf: create swap disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	// Truncate rather than reuse: the guest runs mkswap on it every boot anyway, and starting from a
	// hole means the previous boot's spilled pages stop occupying disk.
	if err := f.Truncate(0); err != nil {
		return "", fmt.Errorf("applevf: reset swap disk: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("applevf: size swap disk: %w", err)
	}
	return path, nil
}

// guestDevices names the guest block devices for the optional disks, in the order they are attached.
//
// The names come from the attach order, not a fixed layout: the read only base is always /dev/vda, and
// each optional disk takes the next letter. A session with no home disk but with swap therefore gets
// swap at /dev/vdb, which is why this is computed rather than hardcoded.
func guestDevices(home, swap string) (homeDevice, swapDevice string) {
	next := 'b'
	name := func(path string) string {
		if path == "" {
			return ""
		}
		device := "/dev/vd" + string(next)
		next++
		return device
	}
	return name(home), name(swap)
}

// homeMountTarget is where the agent's home belongs, taken from the volume mount the session layer adds
// for exactly that purpose. A spec without one gets no persistent home.
func homeMountTarget(mounts []sandbox.Mount) string {
	for _, m := range mounts {
		if m.Type == sandbox.MountVolume {
			return m.Target
		}
	}
	return ""
}

// RemoveStorage deletes a session's home disk. Called when the session itself goes, never when its
// sandbox is merely recreated, which is the whole reason the disk is named by session.
func (d *Driver) RemoveStorage(_ context.Context, name string) error {
	if name == "" {
		return nil
	}
	if err := os.Remove(filepath.Join(d.disks, name+".img")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("applevf: remove home disk: %w", err)
	}
	return nil
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

func (d *Driver) lookup(h sandbox.ID) (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.machines[h]
	if !ok {
		return nil, fmt.Errorf("applevf: unknown sandbox %s: %w", h, sandbox.ErrNotFound)
	}
	return s, nil
}

func (d *Driver) runningMachine(h sandbox.ID) (*machine, error) {
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
	defaultCPUCount uint = 2
	// 4 GiB rather than 2. A coding agent is a big JavaScript runtime, and the writable layer it installs
	// into is tmpfs, so files written inside the VM come out of this same budget. Swap catches a spill
	// now, but swap is a disk: it stops the OOM kill, it does not make 2 GiB feel like 4.
	//
	// A ceiling, not a reservation: the guest only consumes host memory for pages it touches, so a
	// higher one costs an idle VM nothing and lowering it fits no more sessions.
	defaultMemoryBytes uint64 = 4 << 30
)

func cpuCount(r sandbox.Resources) uint {
	if r.CPU <= 0 {
		return defaultCPUCount
	}
	// A VM gets whole vCPUs, so a fractional request rounds UP to at least one: asking for half a core
	// should give you a slow session, not a machine with no processor.
	n := uint(r.CPU)
	if float64(n) < r.CPU {
		n++
	}
	if n == 0 {
		n = 1
	}
	return n
}

func memoryBytes(r sandbox.Resources) uint64 {
	if r.Memory <= 0 {
		return defaultMemoryBytes
	}
	return uint64(r.Memory)
}
