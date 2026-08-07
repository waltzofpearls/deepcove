package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	dockerdrv "github.com/waltzofpearls/deepcove/internal/sandbox/docker"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// runtimeEntry is one runtime this node can host sessions on, ready to be registered. A zero value means
// this machine cannot provide that runtime, which is a normal outcome rather than a failure.
type runtimeEntry struct {
	name    string
	driver  sandbox.Sandbox
	cleanup func()
}

// registerRuntimes gives the service every runtime this machine can host, and returns the cleanup for
// them. It is the single answer to "what can this node run a session on".
//
// Each runtime is BUILT by its own constructor and REGISTERED here, so the list of what a node provides
// reads in one place and every runtime joins it the same way. Which one becomes the default is decided
// afterwards by the caller, from the session package's preference order.
func registerRuntimes(svc *session.Service, cfg *config.Config, store credbroker.Store, logger *slog.Logger) (func(), error) {
	roots := allowedMountRoots()
	container, err := newContainerRuntime(cfg, roots)
	if err != nil {
		return nil, err
	}
	entries := []runtimeEntry{container}
	// The VM runtime is platform specific and depends on artifacts being present, so it declines by
	// returning a zero entry rather than failing the node.
	if vm := newVMRuntime(cfg, roots, store, logger); vm.driver != nil {
		entries = append(entries, vm)
	}

	for _, e := range entries {
		svc.RegisterRuntime(e.name, e.driver)
	}
	return func() {
		for _, e := range entries {
			if e.cleanup != nil {
				e.cleanup()
			}
		}
	}, nil
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
