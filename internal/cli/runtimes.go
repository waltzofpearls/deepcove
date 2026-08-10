package cli

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	dockerdrv "github.com/waltzofpearls/deepcove/internal/sandbox/docker"
	"github.com/waltzofpearls/deepcove/internal/session"
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
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, e := range rs.entries {
		if e.cleanup != nil {
			e.cleanup()
		}
	}
}

// registerRuntimes gives the service every runtime this machine can host, and returns the set so the
// caller can close them. It is the single answer to "what can this node run a session on".
//
// Each runtime is BUILT by its own constructor and REGISTERED here, so the list of what a node provides
// reads in one place and every runtime joins it the same way. Which one becomes the default is decided
// afterwards by the caller, from the session package's preference order.
func registerRuntimes(svc *session.Service, cfg *config.Config, store credbroker.Store, logger *slog.Logger) (*runtimeSet, error) {
	roots := allowedMountRoots()
	container, err := newContainerRuntime(cfg, roots)
	if err != nil {
		return nil, err
	}
	entries := []runtimeEntry{container}
	// The VM runtime is platform specific and depends on artifacts being present, so it declines rather
	// than failing the node. When it declines on a machine that could host VMs it still names itself, so
	// the gap is something the user can see and fix instead of a runtime that appears not to exist.
	vm := newVMRuntime(cfg, roots, store, logger)
	switch {
	case vm.driver != nil:
		entries = append(entries, vm)
	case vm.name != "":
		svc.MarkRuntimeMissing(vm.name, vm.missing)
	}

	set := &runtimeSet{}
	for _, e := range entries {
		svc.RegisterRuntime(e.name, e.driver)
		set.add(e)
	}
	// How to close the gap, if this machine could host VMs but is missing a piece. Registered even when
	// the runtime is already available, since it costs nothing and keeps the wiring in one place.
	registerVMInstaller(svc, cfg, roots, store, logger, set)
	return set, nil
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
