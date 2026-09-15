//go:build linux

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/cloudhv"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmassets"
)

// hostBinaries are the three programs a Linux node runs a VM with.
type hostBinaries struct {
	cloudHypervisor vmAsset
	virtiofsd       vmAsset
	passt           vmAsset
}

// hostCanRunVMs reports whether this machine has usable KVM, and vmSocketDir says where a VM's sockets
// go. Both are variables so a test can answer for a machine it is not running on, and so it never points
// at the directory a live node is using.
var (
	hostCanRunVMs = cloudhv.RequireKVM
	vmSocketDir   = vmIPCDir
)

// newVMRuntime builds the VM runtime this machine can provide. On Linux that is Cloud Hypervisor. A
// machine that cannot run VMs is not an error: it still runs containers, so this returns an empty entry.
func newVMRuntime(deps runtimeDeps) runtimeEntry {
	// First, because a host without KVM can never run VMs. Nothing to download, nothing to offer.
	if err := hostCanRunVMs(); err != nil {
		deps.logger.Info("no vm runtime on this host", "err", err)
		return runtimeEntry{}
	}
	kernel, ok := resolveKernel(deps.cfg)
	if !ok {
		deps.logger.Info("cloud hypervisor runtime needs setup: no guest kernel downloaded",
			"hint", "set it up from the dashboard, or point DEEPCOVE_VM_KERNEL at your own")
		return runtimeEntry{name: session.RuntimeCloudHV, missing: session.MissingKernel}
	}
	binaries, ok := resolveHostBinaries(deps.cfg)
	if !ok {
		deps.logger.Info("cloud hypervisor runtime needs setup: no host binaries downloaded",
			"hint", "set it up from the dashboard")
		return runtimeEntry{name: session.RuntimeCloudHV, missing: session.MissingHostBinaries}
	}
	guestInit, err := resolveGuestInit(deps.cfg)
	if err != nil {
		deps.logger.Info("cloud hypervisor runtime needs setup: no guest init binary", "err", err)
		return runtimeEntry{name: session.RuntimeCloudHV, missing: session.MissingGuestInit}
	}
	ipcDir, err := vmSocketDir()
	if err != nil {
		deps.logger.Warn("cloud hypervisor runtime unavailable", "err", err)
		return runtimeEntry{}
	}

	backend := cloudhv.Backend{
		CloudHypervisor: binaries.cloudHypervisor.path,
		Virtiofsd:       binaries.virtiofsd.path,
		Passt:           binaries.passt.path,
		IPCDir:          ipcDir,
	}
	// Before the driver is built: these VMs are separate processes, so a previous deepcove can have left
	// some running, and the driver is about to sweep the disks they are still using.
	if stopped, err := backend.SweepOrphans(); err != nil {
		deps.logger.Warn("could not clear out the vms a previous run left behind", "err", err)
	} else if stopped > 0 {
		deps.logger.Info("stopped vms left over from a previous run", "processes", stopped)
	}

	driver, err := deps.vmDriver(backend, kernel, guestInit)
	if err != nil {
		deps.logger.Warn("cloud hypervisor runtime unavailable", "err", err)
		return runtimeEntry{}
	}

	ensurePasstCanIsolateItself(deps.cfg, binaries.passt.path, deps.logger)
	deps.logger.Info("cloud hypervisor runtime available", "ipc_dir", ipcDir)
	logVMAssets(deps.logger, kernel, guestInit,
		binaries.cloudHypervisor, binaries.virtiofsd, binaries.passt)
	return vmRuntimeEntry(session.RuntimeCloudHV, driver)
}

// resolveHostBinaries finds the three downloaded programs. All or nothing: a VM needs every one.
func resolveHostBinaries(cfg *config.Config) (hostBinaries, bool) {
	goarch := runtime.GOARCH
	cloudHypervisor, haveCloudHypervisor := downloadedAsset(cfg, vmassets.CloudHypervisor(goarch))
	virtiofsd, haveVirtiofsd := downloadedAsset(cfg, vmassets.Virtiofsd(goarch))
	passt, havePasst := downloadedAsset(cfg, vmassets.Passt(goarch))
	if !haveCloudHypervisor || !haveVirtiofsd || !havePasst {
		return hostBinaries{}, false
	}
	return hostBinaries{cloudHypervisor: cloudHypervisor, virtiofsd: virtiofsd, passt: passt}, true
}

// vmIPCDir holds a running VM's sockets. Not the data dir: under $HOME the path is too long for one.
func vmIPCDir() (string, error) {
	uid := os.Getuid()
	runDir := filepath.Join("/run/user", strconv.Itoa(uid), "deepcove")
	if err := os.MkdirAll(runDir, ipcDirMode); err == nil {
		return runDir, keepPrivate(runDir, uid)
	}
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("deepcove-%d", uid))
	if err := os.MkdirAll(tmpDir, ipcDirMode); err != nil {
		return "", fmt.Errorf("no place to put the VM sockets: %w", err)
	}
	return tmpDir, keepPrivate(tmpDir, uid)
}

// ipcDirMode is what the directory holding a VM's sockets must be. One of those sockets hands out this
// session's credentials, so the directory is the whole of its protection.
const ipcDirMode = 0o700

// keepPrivate refuses a directory another user owns, and tightens one of ours that is open to others.
// In /tmp anyone can create the name first and wait for us to put sockets in it.
func keepPrivate(path string, uid int) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot tell who owns %s", path)
	}
	if int(owner.Uid) != uid {
		return fmt.Errorf("%s belongs to uid %d, not to you, so the VM sockets are not going in it",
			path, owner.Uid)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, ipcDirMode)
}

// ensurePasstCanIsolateItself gets passt the AppArmor profile this host needs, installing it when this
// process is root. Spending the privilege beats working around it: the workaround is passt keeping root
// for the life of every VM, and a network stack is the last thing that should hold it.
//
// Not root, or the install fails: the profile is left in the data dir with the command to load it. passt
// then keeps root if deepcove has it, and cannot start at all if it does not.
func ensurePasstCanIsolateItself(cfg *config.Config, passtBinary string, logger *slog.Logger) {
	if !cloudhv.PasstSandboxNeedsAppArmor() {
		return
	}
	if os.Geteuid() == 0 {
		if err := cloudhv.InstallAppArmorProfile(passtBinary); err == nil {
			logger.Info("installed the apparmor profile passt needs, so it gives up root as usual",
				"path", cloudhv.InstalledAppArmorProfile)
			return
		} else {
			logger.Warn("could not install the apparmor profile passt needs", "err", err)
		}
	}
	path, err := writeAppArmorProfile(cfg.DataDir, passtBinary)
	if err != nil {
		logger.Warn("could not write the apparmor profile passt needs", "err", err)
		return
	}
	logger.Warn("this host stops passt isolating itself, so it runs with the privileges deepcove has",
		"fix", fmt.Sprintf("sudo cp %s %s && sudo apparmor_parser -r %s",
			path, cloudhv.InstalledAppArmorProfile, cloudhv.InstalledAppArmorProfile))
}

// writeAppArmorProfile puts the profile in the data dir, where a root shell can copy it from.
func writeAppArmorProfile(dataDir, passtBinary string) (string, error) {
	path := filepath.Join(dataDir, cloudhv.AppArmorProfileName+".apparmor")
	if err := os.WriteFile(path, []byte(cloudhv.AppArmorProfile(passtBinary)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// registerVMInstaller lets the dashboard download whatever the VM runtime is missing. No restart: it
// ends by building the runtime the same way startup does.
func registerVMInstaller(deps runtimeDeps, set *runtimeSet) {
	deps.svc.SetRuntimeInstaller(session.RuntimeCloudHV,
		func(ctx context.Context, report func(sandbox.PullProgress)) error {
			wanted := append([]vmassets.Artifact{vmassets.GuestKernel(runtime.GOARCH)},
				vmassets.LinuxHost(runtime.GOARCH)...)
			if err := downloadVMAssets(ctx, deps.cfg, wanted, report); err != nil {
				return err
			}

			report(sandbox.PullProgress{Status: "Starting the runtime"})
			return registerInstalledRuntime(ctx, deps.svc, deps.cfg, deps.logger, set, newVMRuntime(deps))
		})
}
