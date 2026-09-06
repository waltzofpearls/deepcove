//go:build darwin

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestasset"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmkernel"
	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

// assetSource says where a VM artifact came from. It is logged at startup, because "which kernel is
// this actually booting" is the first question when a VM behaves in a way the code does not explain.
type assetSource string

const (
	sourceConfigured assetSource = "configured"
	sourceLocalBuild assetSource = "local"
	sourceRelease    assetSource = "release"
	sourceEmbedded   assetSource = "embedded"
)

// vmAsset is one file the VM runtime boots with.
type vmAsset struct {
	name    string
	version string
	source  assetSource
	path    string
}

const (
	kernelAssetName    = "guest-kernel"
	guestInitAssetName = "guest-init"

	// unknownVersion is for a file we did not choose, so its version is whatever the user built.
	unknownVersion = "unknown"
)

// newVMRuntime builds the virtual machine runtime, when this machine can actually provide it. On macOS
// that is Apple VF.
//
// It is deliberately quiet about failure: a node that cannot run VMs still runs sessions in containers,
// which is a working node rather than a broken one. So an unavailable VM runtime is a zero entry, not an
// error, with the reason logged for a user who expected it to be there.
func newVMRuntime(svc *session.Service, cfg *config.Config, allowedRoots []string, store credbroker.Store, logger *slog.Logger, tailnetClient func() *http.Client) runtimeEntry {
	kernel, ok := resolveKernel(cfg)
	if !ok {
		logger.Info("apple vf runtime needs setup: no guest kernel downloaded",
			"hint", "set it up from the dashboard, or point DEEPCOVE_VM_KERNEL at your own")
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingKernel}
	}
	guestInit, err := resolveGuestInit(cfg)
	if err != nil {
		logger.Info("apple vf runtime needs setup: no guest init binary", "err", err)
		return runtimeEntry{name: session.RuntimeAppleVF, missing: session.MissingGuestInit}
	}

	driver, err := applevf.New(kernel.path, guestInit.path, cfg.DataDir,
		applevf.WithCredentialStore(store), applevf.WithLogger(logger),
		applevf.WithAllowedMountRoots(allowedRoots...),
		applevf.WithNetworkAllow(cfg.AllowSandboxHosts...),
		applevf.WithTailnetClient(tailnetClient),
		applevf.WithLLMProxyURL(svc.LLMProxyURL))
	if err != nil {
		logger.Warn("apple vf runtime unavailable", "err", err)
		return runtimeEntry{}
	}

	logger.Info("apple vf runtime available")
	for _, asset := range []vmAsset{kernel, guestInit} {
		logger.Info("vm artifact", "name", asset.name, "version", asset.version,
			"source", string(asset.source), "path", asset.path)
	}
	return runtimeEntry{
		name:    session.RuntimeAppleVF,
		driver:  driver,
		cleanup: func() { _ = driver.Close() },
	}
}

// resolveKernel finds the kernel to boot guests with, without ever going to the network. Startup must
// not wait on a download, and must not fail on a machine that is offline, so a node with no kernel
// reports that it needs setup and the download happens when the user asks for it.
//
// Order: an explicit path, then a local build when DEEPCOVE_LOCAL_VM_ASSETS is set, then the downloaded
// one. A local build is only ever used when asked for, so a developer's leftover kernel cannot quietly
// replace the one this release ships with.
func resolveKernel(cfg *config.Config) (vmAsset, bool) {
	if path := existingFile(cfg.VMKernel); path != "" {
		return vmAsset{
			name: kernelAssetName, version: kernelVersionFromPath(path),
			source: sourceConfigured, path: path,
		}, true
	}
	if cfg.LocalVMAssets {
		if path := existingFile(localKernelPaths()...); path != "" {
			return vmAsset{
				name: kernelAssetName, version: kernelVersionFromPath(path),
				source: sourceLocalBuild, path: path,
			}, true
		}
	}
	path, ok := vmkernel.NewDownloader(kernelDir(cfg), Version).Path()
	if !ok {
		return vmAsset{}, false
	}
	return vmAsset{
		name: kernelAssetName, version: vmruntime.KernelVersion,
		source: sourceRelease, path: path,
	}, true
}

// kernelDir is where downloaded kernels live, alongside the images and disks they are used with.
func kernelDir(cfg *config.Config) string { return filepath.Join(cfg.DataDir, "vm-kernel") }

// localKernelPaths is where a kernel built in this checkout lands. Relative to the binary, not the
// working directory: a service starts wherever launchd or systemd puts it, and a kernel that only
// exists when you happen to be standing in the repo is a silent change of what a session boots.
func localKernelPaths() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), "dist", "kernel", "vmlinux-*"))
	return matches
}

// kernelVersionFromPath reads the version out of a locally built kernel, which is called
// vmlinux-<version>. Any other name is a file someone pointed us at, whose version we cannot know.
func kernelVersionFromPath(path string) string {
	if version, ok := strings.CutPrefix(filepath.Base(path), "vmlinux-"); ok && version != "" {
		return version
	}
	return unknownVersion
}

// resolveGuestInit returns the guest init to boot sessions with. An explicit path wins, for anyone
// testing an init they built themselves. Otherwise the one embedded in this binary is written into the
// data dir, alongside the images and disks it belongs with.
func resolveGuestInit(cfg *config.Config) (vmAsset, error) {
	if cfg.VMGuestInit != "" {
		if info, err := os.Stat(cfg.VMGuestInit); err != nil || info.IsDir() {
			return vmAsset{}, fmt.Errorf("DEEPCOVE_VM_GUEST_INIT is set to %q, which is not a file", cfg.VMGuestInit)
		}
		return vmAsset{
			name: guestInitAssetName, version: unknownVersion,
			source: sourceConfigured, path: cfg.VMGuestInit,
		}, nil
	}
	path, err := guestasset.Install(filepath.Join(cfg.DataDir, "vm-guest"))
	if err != nil {
		return vmAsset{}, err
	}
	// The init ships inside the binary, so it is always exactly this build's.
	return vmAsset{name: guestInitAssetName, version: Version, source: sourceEmbedded, path: path}, nil
}

// existingFile returns the first path that is a file. A configured path that does not exist is treated
// as absent, so a typo degrades to "runtime unavailable" with a logged reason rather than a confusing
// failure at session create time.
func existingFile(candidates ...string) string {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// registerVMInstaller teaches the service how to set up the VM runtime, so a node missing the kernel can
// download it from the dashboard rather than needing a terminal and a restart.
//
// The install ends by building the runtime through the same constructor startup uses, so a runtime set
// up this way is not a second class one.
func registerVMInstaller(
	svc *session.Service,
	cfg *config.Config,
	roots []string,
	store credbroker.Store,
	logger *slog.Logger,
	set *runtimeSet,
	tailnetClient func() *http.Client,
) {
	svc.SetRuntimeInstaller(session.RuntimeAppleVF, func(ctx context.Context, report func(sandbox.PullProgress)) error {
		if _, err := vmkernel.NewDownloader(kernelDir(cfg), Version).Fetch(ctx, func(complete, total int64) {
			report(sandbox.PullProgress{Status: "Downloading kernel", Current: complete, Total: total})
		}); err != nil {
			return err
		}

		report(sandbox.PullProgress{Status: "Starting the runtime"})
		entry := newVMRuntime(svc, cfg, roots, store, logger, tailnetClient)
		if entry.driver == nil {
			// Something else is still missing, so say which rather than reporting a vague failure.
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
	})
}
