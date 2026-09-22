//go:build !windows

package cli

import (
	"context"
	"os/exec"
	"path/filepath"

	"github.com/waltzofpearls/deepcove/internal/sandbox/sandboxless"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// newSandboxlessRuntime builds the runtime that runs sessions on this machine itself. Always offered,
// since every machine can host one. tmux is the exception: it cannot be installed from here, so a
// machine without it gets the runtime named and marked rather than hidden.
func newSandboxlessRuntime(deps runtimeDeps) runtimeEntry {
	if _, err := exec.LookPath("tmux"); err != nil {
		deps.logger.Info("sandboxless runtime needs setup: tmux is not installed",
			"hint", "install tmux with this machine's package manager")
		return runtimeEntry{name: session.RuntimeSandboxless, missing: session.MissingTmux}
	}
	ipcDir, err := ensureIPCDir(deps.cfg.DataDir)
	if err != nil {
		deps.logger.Warn("sandboxless runtime unavailable", "err", err)
		return runtimeEntry{}
	}
	dir := filepath.Join(deps.cfg.DataDir, "sandboxless")
	driver, err := sandboxless.NewDriver(dir, filepath.Join(dir, "agent-home"), ipcDir)
	if err != nil {
		deps.logger.Warn("sandboxless runtime unavailable", "err", err)
		return runtimeEntry{}
	}

	if swept, err := driver.SweepOrphans(context.Background()); err != nil {
		deps.logger.Warn("could not clear out the sessions a previous run left behind", "err", err)
	} else if swept > 0 {
		deps.logger.Info("stopped sessions left over from a previous run", "sessions", swept)
	}

	deps.logger.Info("sandboxless runtime available", "ipc_dir", ipcDir)
	return runtimeEntry{
		name:    session.RuntimeSandboxless,
		driver:  driver,
		cleanup: func() { _ = driver.Close() },
	}
}
