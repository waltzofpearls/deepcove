package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sync"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	dockerdrv "github.com/waltzofpearls/deepcove/internal/sandbox/docker"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmassets"
)

// runtimeEntry is one runtime this node can host sessions on, ready to be registered.
//
// Three shapes, and the difference matters. A driver means it can host sessions. A name with a missing
// reason and no driver means this machine could host them once something is installed, which is worth
// telling the user about. A zero value means the runtime does not apply here at all, like the VM runtime
// off macOS, and saying anything would be noise.
type runtimeEntry struct {
	name    string
	driver  sandbox.Sandbox
	cleanup func()
	// missing is a stable reason code (session.Missing*) set when driver is nil.
	missing string
}

// runtimeSet holds what has been registered, so a runtime installed later can join it and shutdown
// still closes everything. Guarded because an install arrives on an HTTP request, not at startup.
type runtimeSet struct {
	mu      sync.Mutex
	entries []runtimeEntry
}

func (rs *runtimeSet) add(e runtimeEntry) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.entries = append(rs.entries, e)
}

func (rs *runtimeSet) close() {
	for _, e := range rs.snapshot() {
		if e.cleanup != nil {
			e.cleanup()
		}
	}
}

// forceClose abandons the sandboxes of every runtime that can, for a shutdown that already wedged.
func (rs *runtimeSet) forceClose() error {
	var err error
	for _, e := range rs.snapshot() {
		if closer, ok := e.driver.(sandbox.ForceCloser); ok {
			err = errors.Join(err, closer.ForceClose())
		}
	}
	return err
}

// snapshot copies the entries: holding the lock through a teardown would block forceClose behind the
// very hang it exists to break.
func (rs *runtimeSet) snapshot() []runtimeEntry {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return slices.Clone(rs.entries)
}

// registerRuntimes gives the service every runtime this machine can host, and returns the set so the
// caller can close them. It is the single answer to "what can this node run a session on".
//
// Each runtime is BUILT by its own constructor and REGISTERED here, so the list of what a node provides
// reads in one place and every runtime joins it the same way. Which one becomes the default is decided
// afterwards by the caller, from the session package's preference order.
func registerRuntimes(svc *session.Service, cfg *config.Config, store credbroker.Store, logger *slog.Logger, tailnetClient func() *http.Client) (*runtimeSet, error) {
	roots := allowedMountRoots()
	// The service checks a workspace VM's folder against the same list, so a folder it may not share is
	// refused when you pick it rather than when the machine is built.
	svc.SetAllowedMountRoots(roots)
	container, err := newContainerRuntime(cfg, roots)
	if err != nil {
		return nil, err
	}
	entries := []runtimeEntry{container}
	// The VM runtime is platform specific and depends on artifacts being present, so it declines rather
	// than failing the node. When it declines on a machine that could host VMs it still names itself, so
	// the gap is something the user can see and fix instead of a runtime that appears not to exist.
	deps := runtimeDeps{
		svc:           svc,
		cfg:           cfg,
		allowedRoots:  roots,
		creds:         store,
		logger:        logger,
		tailnetClient: tailnetClient,
	}
	vmRuntime := newVMRuntime(deps)
	switch {
	case vmRuntime.driver != nil:
		entries = append(entries, vmRuntime)
	case vmRuntime.name != "":
		svc.MarkRuntimeMissing(vmRuntime.name, vmRuntime.missing)
	}
	sandboxlessRuntime := newSandboxlessRuntime(deps)
	switch {
	case sandboxlessRuntime.driver != nil:
		entries = append(entries, sandboxlessRuntime)
	case sandboxlessRuntime.name != "":
		svc.MarkRuntimeMissing(sandboxlessRuntime.name, sandboxlessRuntime.missing)
	}

	set := &runtimeSet{}
	for _, e := range entries {
		svc.RegisterRuntime(e.name, e.driver)
		set.add(e)
	}
	// How to close the gap, if this machine could host VMs but is missing a piece. Registered even when
	// the runtime is already available, since it costs nothing and keeps the wiring in one place.
	registerVMInstaller(deps, set)
	return set, nil
}

// runtimeDeps are what a runtime constructor is handed: the service it joins, this node's settings, and
// the pieces a sandbox needs from the host. One value rather than six arguments, because every platform
// takes exactly the same ones.
type runtimeDeps struct {
	svc           *session.Service
	cfg           *config.Config
	allowedRoots  []string
	creds         credbroker.Store
	logger        *slog.Logger
	tailnetClient func() *http.Client
}

// vmDriver builds the driver every VM runtime uses. Only the hypervisor differs between platforms;
// everything above it is this.
func (deps runtimeDeps) vmDriver(backend vm.Backend, kernel, guestInit vmAsset) (*vm.Driver, error) {
	return vm.NewDriver(backend, kernel.path, guestInit.path, deps.cfg.DataDir,
		vm.WithCredentialStore(deps.creds),
		vm.WithLogger(deps.logger),
		vm.WithAllowedMountRoots(deps.allowedRoots...),
		vm.WithNetworkAllow(deps.cfg.AllowSandboxHosts...),
		vm.WithTailnetClient(deps.tailnetClient),
		vm.WithLLMProxyURL(deps.svc.LLMProxyURL))
}

// vmRuntimeEntry is a built VM runtime, ready to register.
func vmRuntimeEntry(name string, driver *vm.Driver) runtimeEntry {
	return runtimeEntry{
		name:    name,
		driver:  driver,
		cleanup: func() { _ = driver.Close() },
	}
}

// downloadVMAssets fetches whatever this node is still missing, naming each file as it goes.
func downloadVMAssets(ctx context.Context, cfg *config.Config, wanted []vmassets.Artifact,
	report func(sandbox.PullProgress)) error {
	assets := vmassets.New(artifactDir(cfg), Version)
	for _, artifact := range assets.Missing(wanted) {
		if _, err := assets.Fetch(ctx, artifact, func(complete, total int64) {
			report(sandbox.PullProgress{
				Status: "Downloading " + artifact.Name, Current: complete, Total: total,
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

// registerInstalledRuntime puts a just installed runtime to work, without a restart.
func registerInstalledRuntime(
	ctx context.Context,
	svc *session.Service,
	cfg *config.Config,
	logger *slog.Logger,
	set *runtimeSet,
	entry runtimeEntry,
) error {
	if entry.driver == nil {
		return fmt.Errorf("the runtime still needs setup after downloading: %s", entry.missing)
	}
	svc.RegisterRuntime(entry.name, entry.driver)
	set.add(entry)
	svc.MarkRuntimeReady(entry.name)
	// The node picked its default before this runtime existed. Pick again, or it keeps using Docker
	// until the next restart.
	if err := svc.SelectDefaultRuntime(ctx, cfg.DefaultRuntime); err != nil {
		logger.Warn("keeping the current default runtime after setup",
			"requested", cfg.DefaultRuntime, "err", err)
	}
	return nil
}

// allowedMountRoots are the host directories a session may mount from, applied to every runtime.
//
// $HOME covers the common case of a project under the home dir (~/projects/foo, ~/code). System dirs
// (/etc, /usr, /var, ...) stay blocked because they are not under it. Power users who need to mount
// outside $HOME will get a config flag override as a follow-up. The sandbox layer's mount-source
// validator enforces this.
func allowedMountRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{home}
}

// newContainerRuntime builds the Docker runtime. It is not privileged, just the one runtime available
// wherever a daemon is, so it is always offered and its reachability is judged at use time.
func newContainerRuntime(cfg *config.Config, allowedRoots []string) (runtimeEntry, error) {
	// Scoping containers to this instance's data directory is what keeps "leaked" meaningful: a sandbox
	// with no session row owning it, answerable only against the database this node actually uses.
	driver, err := dockerdrv.New(dockerdrv.NodeScope(cfg.DataDir), dockerdrv.WithAllowedMountRoots(allowedRoots...))
	if err != nil {
		return runtimeEntry{}, fmt.Errorf("docker runtime: %w", err)
	}
	return runtimeEntry{
		name:    session.RuntimeDocker,
		driver:  driver,
		cleanup: func() { _ = driver.Close() },
	}, nil
}
