package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/service"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
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
func registerRuntimes(svc *session.Service, cfg *config.Config, logins *sharedlogin.Keeper, logger *slog.Logger, tailnetClient func() *http.Client) *runtimeSet {
	roots := shareRoots(cfg)
	// The service checks a workspace VM's folder against the same list, so a folder it may not share is
	// refused when you pick it rather than when the machine is built.
	svc.SetShareRoots(roots)
	var entries []runtimeEntry
	// The VM runtime is platform specific and depends on artifacts being present, so it declines rather
	// than failing the node. When it declines on a machine that could host VMs it still names itself, so
	// the gap is something the user can see and fix instead of a runtime that appears not to exist.
	deps := runtimeDeps{
		svc:           svc,
		cfg:           cfg,
		shareRoots:    roots,
		sharedLogin:   logins,
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
	if vmInstallerOffered(vmRuntime.missing) {
		registerVMInstaller(deps, set)
	}
	return set
}

// vmInstallerOffered reports whether a download can close the gap a VM runtime reports. None can give a
// host KVM, and the guest init ships inside deepcove itself. The UI's runtimeSetUpByDeepcove has these
// codes plus no_tmux.
func vmInstallerOffered(missing string) bool {
	return missing != session.MissingKVM && missing != session.MissingGuestInit &&
		missing != session.MissingStartFailed
}

// runtimeDeps are what a runtime constructor is handed: the service it joins, this node's settings, and
// the pieces a sandbox needs from the host. One value rather than six arguments, because every platform
// takes exactly the same ones.
type runtimeDeps struct {
	svc           *session.Service
	cfg           *config.Config
	shareRoots    sandbox.ShareRoots
	sharedLogin   *sharedlogin.Keeper
	logger        *slog.Logger
	tailnetClient func() *http.Client
}

// vmDriver builds the driver every VM runtime uses. Only the hypervisor differs between platforms;
// everything above it is this.
func (deps runtimeDeps) vmDriver(name string, backend vm.Backend, kernel, guestInit vmAsset) (*vm.Driver, error) {
	return vm.NewDriver(backend, kernel.path, guestInit.path, deps.cfg.DataDir,
		vm.WithLogger(deps.logger),
		vm.WithShareRoots(deps.shareRoots),
		vm.WithNetworkAllow(deps.cfg.AllowSandboxHosts...),
		vm.WithTailnetClient(deps.tailnetClient),
		vm.WithLLMProxyURL(deps.svc.LLMProxyURL),
		vm.WithAPIKeys(deps.svc.APIKeyFor),
		vm.WithSharedLogin(deps.sharedLogin),
		vm.WithOnSilentGuestStopped(func(sandboxID sandbox.ID) {
			deps.svc.MarkSandboxStopped(name, sandboxID)
		}))
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
		// The reason may have changed, and the UI should stop offering a setup that just ran.
		if entry.missing != "" {
			svc.MarkRuntimeMissing(entry.name, entry.missing)
		}
		return errors.New(`the download finished, but the runtime still cannot start; see deepcove's output in its terminal, or run "deepcove service status" if it runs as a service`)
	}
	svc.RegisterRuntime(entry.name, entry.driver)
	set.add(entry)
	svc.MarkRuntimeReady(entry.name)
	// The node picked its default before this runtime existed, and may have had none at all. Pick again,
	// or it stays that way until the next restart.
	if err := svc.SelectDefaultRuntime(ctx, cfg.DefaultRuntime); err != nil {
		logger.Warn("requested default runtime is not provided on this node; picking the best default it has, which may be none",
			"requested", cfg.DefaultRuntime, "err", err)
	}
	// Its sessions were skipped at startup. Not on the request's context: the runtime is registered now,
	// so a client that leaves here must not cancel bringing them back.
	if lost := svc.ReconcileSessions(context.WithoutCancel(ctx), entry.name); len(lost) > 0 {
		svc.RestartSessionsInBackground(lost)
	}
	return nil
}

// shareRoots are the host folders a session may share, applied to every runtime.
//
// $HOME covers the common case of a project under the home dir (~/projects/foo, ~/code). System dirs
// (/etc, /usr, /var, ...) stay blocked because they are not under it. Power users who need to share
// outside $HOME will get a config flag override as a follow-up. The sandbox layer's host path
// validator enforces this.
//
// Refused: the data dir (tailnet key, logins), and the files deepcove runs from (its program, its service
// file, a custom kernel). A guest that could change them could run code on this machine.
func shareRoots(cfg *config.Config) sandbox.ShareRoots {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return sandbox.ShareRoots{}
	}
	refused := []string{cfg.DataDir}
	// No sandbox pastes land here, for an agent that runs on this machine.
	if cache, err := os.UserCacheDir(); err == nil {
		refused = append(refused, filepath.Join(cache, "deepcove"))
	}
	if exe, err := os.Executable(); err == nil {
		programs := []string{exe}
		// Started through a link (Homebrew does this), the real program is somewhere else. Refuse both.
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			programs = append(programs, real)
		}
		for _, program := range programs {
			// Guarding a program built in its own source checkout protects nothing: a session that can
			// reach it can already change the source it is built from.
			if !builtInSourceCheckout(program) {
				refused = append(refused, program)
			}
		}
	}
	refused = append(refused, service.ProtectedFolders(home)...)
	for _, file := range []string{cfg.VMKernel, cfg.VMGuestInit} {
		if file != "" {
			refused = append(refused, file)
		}
	}
	// A kernel built in this checkout boots every VM too.
	if cfg.LocalVMAssets {
		refused = append(refused, localKernelPaths()...)
	}
	for i, path := range refused {
		if abs, err := filepath.Abs(path); err == nil {
			refused[i] = abs
		}
	}
	return sandbox.ShareRoots{Allowed: []string{home}, Refused: refused}
}

// builtInSourceCheckout reports whether program sits next to deepcove's own go.mod, which is where
// `make build` puts it.
func builtInSourceCheckout(program string) bool {
	goMod, err := os.ReadFile(filepath.Join(filepath.Dir(program), "go.mod"))
	if err != nil {
		return false
	}
	firstLine, _, _ := strings.Cut(string(goMod), "\n")
	return strings.TrimSpace(firstLine) == "module github.com/waltzofpearls/deepcove"
}
