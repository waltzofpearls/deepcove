//go:build darwin

package cli

import (
	"context"
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

	driver, err := deps.vmDriver(applevf.Backend{Logger: deps.logger}, kernel, guestInit)
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
