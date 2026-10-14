package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/llmproxy"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// Driver implements sandbox.Sandbox by running each session in its own virtual machine. The hypervisor
// is a Backend.
//
// Session lifecycle maps onto the VM lifecycle: Create resolves and builds the read only base image
// (cached per digest, so only the first session for an image pays for it), Start boots the VM, Stop asks
// the guest to power off over the control channel, and Destroy releases everything.
type Driver struct {
	// The agent answers a guest runtime shares. Setup is in agent.go, since it reads this driver's guest.
	sandbox.SandboxedProvisioner
	backend   Backend
	images    *imageStore
	disks     string
	logs      string
	kernel    string
	guestInit string
	logger    *slog.Logger
	// shareRoots bounds which host folders may be shared into a VM. Empty Allowed means none may be.
	shareRoots sandbox.ShareRoots

	// networkAllow are private addresses a sandbox may still reach. Empty is the default and the only
	// value a public build can have: it comes from a command line flag and nothing else.
	networkAllow []string

	// tailnetClient reaches a proxy on the tailnet. A function, not a client: the tailnet node is built
	// after the runtimes and may connect later still.
	tailnetClient func() *http.Client

	// llmProxyURL is asked on every request, not once at boot, so changing it in the UI reaches machines
	// that are already running. Nil, or an empty answer, makes the forwarder refuse with an error the
	// agent can read.
	llmProxyURL func() string
	// apiKeyFor is the user's API key for a session on a VM that uses it. Asked per request.
	apiKeyFor func(sandboxID sandbox.ID, sessionID string) (string, error)

	silentGuestCheckEvery time.Duration
	silentGuestLimit      time.Duration
	// onSilentGuestStopped hears about a VM the driver stopped on its own, so the session can say so.
	onSilentGuestStopped func(sandbox.ID)

	// sharedLogin keeps the user's own agent logins for the sessions that share them. Nil shares none.
	sharedLogin *sharedlogin.Keeper

	mu        sync.Mutex
	sandboxes map[sandbox.ID]*sandboxState
}

// sandboxState is one created sandbox: its spec, its resolved base image, and the VM once started.
type sandboxState struct {
	spec      sandbox.SandboxSpec
	baseImage string
	machine   *Guest
	// starting is set while Start is booting this VM. Guarded by the driver's mutex, like machine.
	starting  bool
	createdAt time.Time
}

// Option configures a Driver.
type Option func(*Driver)

// WithLogger records VM lifecycle transitions, which tie each VM to its session in the log.
func WithLogger(logger *slog.Logger) Option {
	return func(d *Driver) { d.logger = logger }
}

// WithShareRoots sets which host folders may be shared into a VM. Any other share is refused at Create,
// Start and Restart.
func WithShareRoots(roots sandbox.ShareRoots) Option {
	return func(d *Driver) { d.shareRoots = roots }
}

// WithNetworkAllow lets sandboxes reach the given private addresses, which are otherwise blocked. Each
// entry is an IP or a CIDR. The only caller is the command line flag, deliberately: there is no API or
// stored setting that can open one of these.
func WithNetworkAllow(hosts ...string) Option {
	return func(d *Driver) { d.networkAllow = hosts }
}

// WithTailnetClient gives the driver a way to reach a proxy that lives on the tailnet.
func WithTailnetClient(client func() *http.Client) Option {
	return func(d *Driver) { d.tailnetClient = client }
}

// WithLLMProxyURL tells the driver where to forward LLM requests. Read per request.
func WithLLMProxyURL(current func() string) Option {
	return func(d *Driver) { d.llmProxyURL = current }
}

// WithAPIKeys gives the driver the user's API key for each session that uses it. Read per request.
func WithAPIKeys(apiKeyFor func(sandboxID sandbox.ID, sessionID string) (string, error)) Option {
	return func(d *Driver) { d.apiKeyFor = apiKeyFor }
}

// apiKeysOn answers for the sessions on one VM, so a request naming a session elsewhere gets no key.
func (d *Driver) apiKeysOn(sandboxID sandbox.ID) func(sessionID string) (string, error) {
	if d.apiKeyFor == nil {
		return nil
	}
	return func(sessionID string) (string, error) { return d.apiKeyFor(sandboxID, sessionID) }
}

// LLMEndpoint implements sandbox.LLMForwarder.
func (d *Driver) LLMEndpoint() string { return llmproxy.LLMEndpoint() }

// WithSharedLogin lets sessions that share their login get it from keeper.
func WithSharedLogin(keeper *sharedlogin.Keeper) Option {
	return func(d *Driver) { d.sharedLogin = keeper }
}

// WithOnSilentGuestStopped is told about each VM the driver stopped because its guest went silent.
func WithOnSilentGuestStopped(stopped func(sandbox.ID)) Option {
	return func(d *Driver) { d.onSilentGuestStopped = stopped }
}

// NewDriver builds a Driver on a hypervisor. kernelPath is the guest kernel (a Kata style build with
// virtio and overlayfs compiled in), guestInitPath is the compiled Linux init binary baked into every
// image, and stateDir is where this runtime keeps what it owns: cached base images, and the per session
// home disks.
func NewDriver(backend Backend, kernelPath, guestInitPath, stateDir string, opts ...Option) (*Driver, error) {
	if kernelPath == "" {
		return nil, fmt.Errorf("vm: a guest kernel path is required")
	}
	if guestInitPath == "" {
		return nil, fmt.Errorf("vm: a guest init binary path is required")
	}
	images, err := newImageStore(filepath.Join(stateDir, "vm-images"), baseInjection(guestInitPath))
	if err != nil {
		return nil, fmt.Errorf("vm: %w", err)
	}
	d := &Driver{
		backend: backend,
		// Only the constant parts go in the shared, digest cached base image. The per session entrypoint
		// is served over vsock at boot instead, so sessions using the same image still share one build.
		images:    images,
		disks:     filepath.Join(stateDir, "vm-disks"),
		logs:      filepath.Join(stateDir, "logs"),
		kernel:    kernelPath,
		guestInit: guestInitPath,
		sandboxes: map[sandbox.ID]*sandboxState{},

		silentGuestCheckEvery: silentGuestCheckEvery,
		silentGuestLimit:      silentGuestLimit,
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.logger == nil {
		d.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	d.sweepSwapDisks()
	d.sweepOldConsoleLogFiles(time.Now())
	if removed := d.images.sweepUnused(time.Now()); removed > 0 {
		d.logger.Info("removed base images nothing had booted in a while", "count", removed)
	}
	return d, nil
}

// sweepSwapDisks removes swap disks left by a previous process.
//
// Swap is worthless across a boot, and Destroy only removes the swap of a machine it tore down itself,
// so without this they accumulate one per sandbox forever. It runs at startup, when no VM of ours is
// live: a backend whose VMs can outlive the process puts them down before the driver is built.
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
	_ sandbox.SpecUpdater  = (*Driver)(nil)
)

// Create resolves the session's image into a cached read only ext4 base and records the spec. The VM is
// not booted until Start.
func (d *Driver) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.ID, error) {
	if err := d.validateSpec(spec); err != nil {
		return "", err
	}
	base, err := d.images.baseImage(ctx, spec.Image)
	if err != nil {
		return "", err
	}
	handle := sandbox.ID(newHandleID())

	d.mu.Lock()
	defer d.mu.Unlock()
	d.sandboxes[handle] = &sandboxState{spec: spec, baseImage: base, createdAt: time.Now()}
	return handle, nil
}

// Start boots the VM for a created sandbox.
func (d *Driver) Start(ctx context.Context, sandboxID sandbox.ID) error {
	state, err := d.lookup(sandboxID)
	if err != nil {
		return err
	}
	// Check again: a guest may have swapped a shared folder for a link since Create. Links are resolved
	// once, and the folder that passes the check is the one the VM gets.
	spec := withResolvedShares(state.spec)
	if err := d.validateSpec(spec); err != nil {
		return err
	}
	d.mu.Lock()
	started := state.machine != nil
	state.starting = !started
	d.mu.Unlock()
	if started {
		return fmt.Errorf("vm: sandbox %s is already started: %w", sandboxID, sandbox.ErrConflict)
	}
	defer func() {
		d.mu.Lock()
		state.starting = false
		d.mu.Unlock()
	}()

	shares, guestMounts := hostShares(spec.Mounts)
	shares, closeSharedFolders, err := openSharedFolders(shares, d.shareRoots)
	if err != nil {
		return err
	}
	defer closeSharedFolders()
	home, err := d.homeDisk(state.spec)
	if err != nil {
		return err
	}
	memory := memoryBytes(state.spec.Resources)
	swap, err := d.swapDisk(sandboxID, memory)
	if err != nil {
		return err
	}
	booted, err := d.backend.Boot(Spec{
		KernelPath:   d.kernel,
		Cmdline:      DefaultCmdline().String(),
		BaseImage:    state.baseImage,
		ScratchImage: home,
		SwapImage:    swap,
		CPUCount:     cpuCount(state.spec.Resources),
		MemoryBytes:  memory,
		Shares:       shares,
		HostPorts:    []uint32{llmproxy.HostVSockPort, guestcfg.Port, sharedlogin.Port},
		NetworkAllow: d.networkAllow,
	})
	if err != nil {
		return err
	}
	guest := NewGuest(booted, d.logger)
	// Recorded before it is ready: a hypervisor owns its VM as a separate process, so one booted while
	// the node is shutting down has to be reachable by Close or it outlives deepcove.
	d.mu.Lock()
	state.machine = guest
	d.mu.Unlock()

	homeDevice, swapDevice := guestDevices(home, swap)
	if err := guest.ServeLLMForward(d.llmProxyURL, d.apiKeysOn(sandboxID), d.tailnetClient); err != nil {
		return d.abandon(state, guest, err)
	}
	sharedLoginAgent := ""
	// Served whether or not the session shares now: the keeper asks on every request, so the switch
	// works without a restart.
	if d.sharedLogin != nil && d.sharedLogin.CanShare(state.spec.Agent.Name) {
		sharedLoginAgent = state.spec.Agent.Name
		if err := guest.ServeSharedLogin(d.sharedLogin, sharedLoginAgent, state.spec.SessionID, sandboxID); err != nil {
			return d.abandon(state, guest, err)
		}
	}
	// The guest asks for these at boot, so they have to be listening before it gets that far.
	if err := guest.ServeConfig(guestcfg.Config{
		Entrypoint:       state.spec.Cmd,
		Env:              state.spec.Env,
		OverlayBytes:     state.spec.Resources.Disk,
		Mounts:           guestMounts,
		HomeDevice:       homeDevice,
		SwapDevice:       swapDevice,
		NetworkAllow:     d.networkAllow,
		LLMRequestPort:   llmproxy.LLMRequestPort,
		SharedLoginAgent: sharedLoginAgent,
	}); err != nil {
		return d.abandon(state, guest, err)
	}

	// Drain the console before waiting: it is an OS pipe, so an undrained one eventually blocks the
	// guest mid boot, and its contents are what explain a failure to come up.
	guest.CaptureConsole(d.consolePath(sandboxID))

	// Do not report a running session until the guest actually answers. Otherwise a guest that panicked
	// still looks healthy and only reveals itself later as an empty terminal.
	if err := guest.WaitReady(ctx, bootTimeout); err != nil {
		return d.abandon(state, guest, err)
	}

	d.mu.Lock()
	ours := state.machine == guest
	live := d.liveCountLocked()
	d.mu.Unlock()
	if !ours {
		return fmt.Errorf("vm: sandbox %s was stopped while it was starting", sandboxID)
	}
	go d.watchForSilence(sandboxID, guest)
	d.logger.Info("vm started", "handle", sandboxID, "image", state.spec.Image, "live_vms", live)
	return nil
}

// abandon puts down a machine that failed to finish starting, and forgets it unless a shutdown already
// took it away.
func (d *Driver) abandon(state *sandboxState, m *Guest, cause error) error {
	d.mu.Lock()
	if state.machine == m {
		state.machine = nil
	}
	d.mu.Unlock()
	return errors.Join(cause, m.Stop())
}

// consoleLogLife is how long a dead VM's console is kept. Long enough to read after the fact, short
// enough that the directory does not grow one file per VM forever.
const consoleLogLife = 30 * 24 * time.Hour

// sweepOldConsoleLogFiles deletes consoles nothing has written to in a while. Startup only, when this
// node has no VM running and so cannot be writing one.
func (d *Driver) sweepOldConsoleLogFiles(now time.Time) {
	matches, err := filepath.Glob(filepath.Join(d.logs, handlePrefix+"*.log"))
	if err != nil {
		return
	}
	removed := 0
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || now.Sub(info.ModTime()) < consoleLogLife {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	if removed > 0 {
		d.logger.Info("removed old vm consoles", "count", removed)
	}
}

// consolePath is where a VM's console is kept, so a guest that dies leaves something to read.
func (d *Driver) consolePath(sandboxID sandbox.ID) string {
	if err := os.MkdirAll(d.logs, 0o755); err != nil {
		return ""
	}
	return filepath.Join(d.logs, string(sandboxID)+".log")
}

// ListSandboxes reports every VM this driver tracks. The registry is in memory and dies with the
// process, so after a restart this is legitimately empty while the session table still names handles.
// That is not a bug to paper over: those sessions really have lost their VMs, and the view should say so.
func (d *Driver) ListSandboxes(context.Context) ([]sandbox.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]sandbox.Instance, 0, len(d.sandboxes))
	for sandboxID, state := range d.sandboxes {
		instanceState := sandbox.InstanceStopped
		switch {
		case state.machine != nil:
			instanceState = sandbox.InstanceRunning
		case state.starting:
			instanceState = sandbox.InstanceStarting
		}
		out = append(out, sandbox.Instance{
			ID:           sandboxID,
			State:        instanceState,
			Unresponsive: state.machine != nil && state.machine.Unresponsive(),
			Image:        state.spec.Image,
			Mounts:       slices.Clone(state.spec.Mounts),
			Resources:    actualResources(state.spec.Resources),
			CreatedAt:    state.createdAt,
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

// hostShares turns the spec's mounts into virtio-fs shares and the matching guest side mount list.
func hostShares(mounts []sandbox.Mount) ([]Share, []guestcfg.Mount) {
	var shares []Share
	var guest []guestcfg.Mount
	for _, m := range mounts {
		tag := shareTag(len(shares))
		shares = append(shares, Share{Tag: tag, HostPath: m.Source, ReadOnly: m.ReadOnly})
		guest = append(guest, guestcfg.Mount{Tag: tag, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	return shares, guest
}

// withResolvedShares is spec with every shared folder's links followed, so the path that is checked and
// the path the VM opens are the same string.
func withResolvedShares(spec sandbox.SandboxSpec) sandbox.SandboxSpec {
	mounts := make([]sandbox.Mount, len(spec.Mounts))
	for i, m := range spec.Mounts {
		m.Source = sandbox.ResolveSymlinks(m.Source)
		mounts[i] = m
	}
	spec.Mounts = mounts
	return spec
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
	for _, state := range d.sandboxes {
		if state.machine != nil {
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

// Stop asks the guest to power itself off, so it can flush its disks first. Stopping an already stopped
// sandbox is not an error.
func (d *Driver) Stop(_ context.Context, sandboxID sandbox.ID) error {
	state, err := d.lookup(sandboxID)
	if err != nil {
		return err
	}
	d.mu.Lock()
	m := state.machine
	state.machine = nil
	d.mu.Unlock()
	if m == nil {
		return nil
	}
	err = m.Stop()

	d.mu.Lock()
	live := d.liveCountLocked()
	d.mu.Unlock()
	d.logger.Info("vm stopped", "handle", sandboxID, "live_vms", live, "err", err)
	return err
}

// Destroy stops the VM if it is running and forgets the sandbox. The cached base image survives, because
// it is shared by every session using that image.
func (d *Driver) Destroy(ctx context.Context, sandboxID sandbox.ID) error {
	if err := d.Stop(ctx, sandboxID); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.sandboxes, sandboxID)
	live := d.liveCountLocked()
	tracked := len(d.sandboxes)
	d.mu.Unlock()
	// Swap belongs to the machine, so it goes with it. The home disk does not: it is keyed by session and
	// outlives the sandbox on purpose.
	if err := os.Remove(filepath.Join(d.disks, "swap-"+string(sandboxID)+".img")); err != nil && !os.IsNotExist(err) {
		d.logger.Warn("could not remove swap disk", "handle", sandboxID, "err", err)
	}
	d.logger.Info("vm destroyed", "handle", sandboxID, "live_vms", live, "tracked_sandboxes", tracked)
	return nil
}

// Exec runs a command inside a started VM over vsock.
func (d *Driver) Exec(ctx context.Context, sandboxID sandbox.ID, cmd sandbox.ExecSpec) (sandbox.Process, error) {
	m, err := d.runningMachine(sandboxID)
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
func (d *Driver) Logs(_ context.Context, sandboxID sandbox.ID, _ sandbox.LogOpts) (io.ReadCloser, error) {
	m, err := d.runningMachine(sandboxID)
	if err != nil {
		return nil, err
	}
	// The captured buffer, not the live console: the driver already drains that, and two readers would
	// each get a fraction of the output. LogOpts is ignored because this is a bounded tail, not a
	// replayable stream.
	return io.NopCloser(bytes.NewReader(m.ConsoleTail())), nil
}

// Ping reports whether this host can run VMs at all, which is what the preflight check surfaces.
func (d *Driver) Ping(context.Context) (string, error) {
	return d.backend.Version()
}

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
// a VM needs a guest side helper that does not exist yet.
func (d *Driver) CopyIn(context.Context, sandbox.ID, string, string) error {
	return fmt.Errorf("vm: copy into a VM is not implemented: %w", sandbox.ErrNotSupported)
}

func (d *Driver) CopyOut(context.Context, sandbox.ID, string, string) error {
	return fmt.Errorf("vm: copy out of a VM is not implemented: %w", sandbox.ErrNotSupported)
}

// Snapshot and Restore are optional; the session layer falls back to cold starting a fresh VM.
func (d *Driver) Snapshot(context.Context, sandbox.ID) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (d *Driver) Restore(context.Context, sandbox.ID, sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// validateSpec is the check Create, Start and Restart share. A folder shared at or under the guest home
// would hide the home disk or a piece of it, so it is refused here as well as in the session layer.
func (d *Driver) validateSpec(spec sandbox.SandboxSpec) error {
	if err := sandbox.ValidateSpec(spec, d.shareRoots); err != nil {
		return err
	}
	if len(spec.Cmd) == 0 {
		return fmt.Errorf("vm: spec.Cmd is required; it is the session entrypoint the guest runs")
	}
	for _, m := range spec.Mounts {
		target := filepath.Clean(m.Target)
		if target == sandbox.SandboxedAgentHome || strings.HasPrefix(target, sandbox.SandboxedAgentHome+"/") {
			return fmt.Errorf("vm: %s is the agent's home, so no folder can be shared at %s",
				sandbox.SandboxedAgentHome, m.Target)
		}
	}
	return nil
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
	if spec.SessionID == "" {
		return "", nil
	}
	if err := os.MkdirAll(d.disks, 0o700); err != nil {
		return "", fmt.Errorf("vm: create disk dir: %w", err)
	}
	path := filepath.Join(d.disks, spec.SessionID+".img")
	switch _, err := os.Stat(path); {
	case err == nil:
		return path, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("vm: stat home disk: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("vm: create home disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(homeDiskBytes); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("vm: size home disk: %w", err)
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
func (d *Driver) swapDisk(sandboxID sandbox.ID, memory uint64) (string, error) {
	size := swapDiskBytes(memory)
	if size <= 0 {
		return "", nil
	}
	if err := os.MkdirAll(d.disks, 0o700); err != nil {
		return "", fmt.Errorf("vm: create disk dir: %w", err)
	}
	path := filepath.Join(d.disks, "swap-"+string(sandboxID)+".img")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("vm: create swap disk: %w", err)
	}
	defer func() { _ = f.Close() }()
	// Truncate rather than reuse: the guest runs mkswap on it every boot anyway, and starting from a
	// hole means the previous boot's spilled pages stop occupying disk.
	if err := f.Truncate(0); err != nil {
		return "", fmt.Errorf("vm: reset swap disk: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("vm: size swap disk: %w", err)
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

// RemoveStorage deletes a session's home disk. Called when the session itself goes, never when its
// sandbox is merely recreated, which is the whole reason the disk is named by session.
func (d *Driver) RemoveStorage(_ context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := os.Remove(filepath.Join(d.disks, sessionID+".img")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vm: remove home disk: %w", err)
	}
	return nil
}

// ForceClose abandons every running VM without asking its guest, for a shutdown that already wedged.
func (d *Driver) ForceClose() error {
	return stopAll(d.takeRunning(), (*Guest).ForceStop)
}

// closeBudget is how long the guests get to shut themselves down before they are forced.
const closeBudget = 8 * time.Second

// Close stops every running VM. Used when the process shuts down.
func (d *Driver) Close() error {
	return d.closeWithin(closeBudget)
}

// closeWithin asks the guests to stop, and forces down whatever is still up when the budget runs out.
// Shutdown then takes a known amount of time, instead of however long a dead guest feels like taking.
func (d *Driver) closeWithin(budget time.Duration) error {
	running := d.takeRunning()
	stopped := make(chan error, 1)
	go func() { stopped <- stopAll(running, (*Guest).Stop) }()

	select {
	case err := <-stopped:
		return err
	case <-time.After(budget):
		return errors.Join(
			fmt.Errorf("vm: the guests did not stop within %s, forcing them down", budget),
			stopAll(running, (*Guest).ForceStop),
		)
	}
}

// takeRunning hands over every running VM and forgets them, so two callers cannot stop the same one.
func (d *Driver) takeRunning() []*Guest {
	d.mu.Lock()
	defer d.mu.Unlock()
	running := make([]*Guest, 0, len(d.sandboxes))
	for _, state := range d.sandboxes {
		if state.machine != nil {
			running = append(running, state.machine)
			state.machine = nil
		}
	}
	return running
}

// stopAll stops every VM at once: one at a time would blow the shutdown budget.
func stopAll(running []*Guest, stop func(*Guest) error) error {
	errs := make([]error, len(running))
	var wg sync.WaitGroup
	for i, m := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = stop(m)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Restart replaces the stored spec, then stops and starts. Start builds the VM from that spec, so a new
// size or folder takes effect.
func (d *Driver) Restart(ctx context.Context, sandboxID sandbox.ID, spec sandbox.SandboxSpec) error {
	if err := d.UpdateSpec(ctx, sandboxID, spec); err != nil {
		return err
	}
	if err := d.Stop(ctx, sandboxID); err != nil {
		return err
	}
	return d.Start(ctx, sandboxID)
}

// UpdateSpec replaces the spec the VM boots from next. A running VM keeps going as it is.
//
// The spec goes through the same checks as Create. Skipping them would make this a way to mount a host
// folder this node refuses to share, and would leave a renamed image booting the old rootfs.
func (d *Driver) UpdateSpec(ctx context.Context, sandboxID sandbox.ID, spec sandbox.SandboxSpec) error {
	state, err := d.lookup(sandboxID)
	if err != nil {
		return err
	}
	if err := d.validateSpec(spec); err != nil {
		return err
	}
	base, err := d.images.baseImage(ctx, spec.Image)
	if err != nil {
		return err
	}
	d.mu.Lock()
	state.spec = spec
	state.baseImage = base
	d.mu.Unlock()
	return nil
}

func (d *Driver) lookup(sandboxID sandbox.ID) (*sandboxState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.sandboxes[sandboxID]
	if !ok {
		return nil, fmt.Errorf("vm: unknown sandbox %s: %w", sandboxID, sandbox.ErrNotFound)
	}
	return state, nil
}

func (d *Driver) runningMachine(sandboxID sandbox.ID) (*Guest, error) {
	state, err := d.lookup(sandboxID)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if state.machine == nil {
		return nil, fmt.Errorf("vm: sandbox %s is not running: %w", sandboxID, sandbox.ErrConflict)
	}
	return state.machine, nil
}

// Resource defaults. A VM must be given concrete values, so an unset limit becomes a sensible default
// rather than zero.
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
