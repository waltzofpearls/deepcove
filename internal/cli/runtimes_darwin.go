//go:build darwin

package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmassets"
)

// newVMRuntime builds the VM runtime this machine can provide. On macOS that is Apple VF. When it can't,
// it returns the runtime's name and what is missing, so the UI can say why.
func newVMRuntime(deps runtimeDeps) runtimeEntry {
	// Before the kernel: the guest init ships inside deepcove, so no download can fix it, and a node
	// missing both must not offer a setup that cannot finish.
	guestInit, err := resolveGuestInit(deps.cfg)
	if err != nil {
		deps.logger.Info("apple vf runtime needs setup: no guest init binary", "err", err)
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingGuestInit}
	}
	kernel, ok := resolveKernel(deps.cfg)
	if !ok {
		deps.logger.Info("apple vf runtime needs setup: no guest kernel downloaded",
			"hint", "set it up from the dashboard, or point DEEPCOVE_VM_KERNEL at your own")
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingKernel}
	}

	self, err := os.Executable()
	if err != nil {
		deps.logger.Warn("apple vf runtime unavailable: cannot find this binary", "err", err)
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingStartFailed}
	}
	ipcDir, err := ensureIPCDir(deps.cfg.DataDir)
	if err != nil {
		deps.logger.Warn("apple vf runtime unavailable", "err", err)
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingStartFailed}
	}
	vmDir := filepath.Join(ipcDir, "vm")
	// Before the driver is built: it clears out disks, and a VM a crashed deepcove left could still use them.
	if stopped, err := applevf.SweepLeftovers(vmDir); err != nil {
		deps.logger.Warn("could not clear out the vms a previous run left behind", "err", err)
	} else if stopped > 0 {
		deps.logger.Info("stopped vms left over from a previous run", "processes", stopped)
	}
	backend := applevf.Backend{Self: self, IPCDir: vmDir}
	driver, err := deps.vmDriver(session.RuntimeAppleVF, backend, kernel, guestInit)
	if err != nil {
		deps.logger.Warn("apple vf runtime unavailable", "err", err)
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingStartFailed}
	}

	deps.logger.Info("apple vf runtime available")
	logVMAssets(deps.logger, kernel, guestInit)
	return vmRuntimeEntry(session.RuntimeAppleVF, driver)
}

// registerVMInstaller lets the dashboard download the kernel a node is missing. No restart: it ends by
// building the runtime the same way startup does.
func registerVMInstaller(deps runtimeDeps, set *runtimeSet) {
	deps.svc.SetRuntimeInstaller(session.RuntimeAppleVF,
		func(ctx context.Context, report func(sandbox.PullProgress)) error {
			wanted := []vmassets.Artifact{vmassets.GuestKernel(runtime.GOARCH)}
			if err := downloadVMAssets(ctx, deps.cfg, wanted, report); err != nil {
				return err
			}

			report(sandbox.PullProgress{Status: "Starting the runtime"})
			return registerInstalledRuntime(ctx, deps.svc, deps.cfg, deps.logger, set, newVMRuntime(deps))
		})
}
