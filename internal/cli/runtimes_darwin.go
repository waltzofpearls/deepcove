//go:build darwin

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestasset"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmkernel"
)

// newVMRuntime builds the virtual machine runtime, when this machine can actually provide it. On macOS
// that is Apple VF.
//
// It is deliberately quiet about failure: a node that cannot run VMs still runs sessions in containers,
// which is a working node rather than a broken one. So an unavailable VM runtime is a zero entry, not an
// error, with the reason logged for a user who expected it to be there.
func newVMRuntime(cfg *config.Config, allowedRoots []string, store credbroker.Store, logger *slog.Logger) runtimeEntry {
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

	driver, err := applevf.New(kernel, guestInit, cfg.DataDir,
		applevf.WithCredentialStore(store), applevf.WithLogger(logger),
		applevf.WithAllowedMountRoots(allowedRoots...))
	if err != nil {
		logger.Warn("apple vf runtime unavailable", "err", err)
		return runtimeEntry{}
	}

	logger.Info("apple vf runtime available", "kernel", kernel, "guest_init", guestInit)
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
// Order: an explicit path, then whatever has already been downloaded, then the developer's dist/ build.
func resolveKernel(cfg *config.Config) (string, bool) {
	if path := firstExisting(cfg.VMKernel, defaultKernelPaths()...); path != "" {
		return path, true
	}
	return vmkernel.NewStore(kernelDir(cfg)).Cached()
}

// kernelDir is where downloaded kernels live, alongside the images and disks they are used with.
func kernelDir(cfg *config.Config) string { return filepath.Join(cfg.DataDir, "vm-kernel") }

// defaultKernelPaths is where a developer's own build lands, so a checkout that has one keeps working
// without downloading anything.
func defaultKernelPaths() []string {
	matches, _ := filepath.Glob("dist/kernel/vmlinux-*")
	return matches
}

// resolveGuestInit returns the guest init to boot sessions with. An explicit path wins, for anyone
// testing an init they built themselves. Otherwise the one embedded in this binary is written into the
// data dir, alongside the images and disks it belongs with.
func resolveGuestInit(cfg *config.Config) (string, error) {
	if cfg.VMGuestInit != "" {
		if info, err := os.Stat(cfg.VMGuestInit); err != nil || info.IsDir() {
			return "", fmt.Errorf("DEEPCOVE_VM_GUEST_INIT is set to %q, which is not a file", cfg.VMGuestInit)
		}
		return cfg.VMGuestInit, nil
	}
	return guestasset.Install(filepath.Join(cfg.DataDir, "vm-guest"))
}

// firstExisting returns the first path that exists, preferring an explicit configured value. An
// explicitly configured path that does not exist is treated as absent, so a typo degrades to "runtime
// unavailable" with a logged reason rather than a confusing failure at session create time.
func firstExisting(configured string, fallbacks ...string) string {
	candidates := fallbacks
	if configured != "" {
		candidates = append([]string{configured}, fallbacks...)
	}
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
) {
	svc.SetRuntimeInstaller(session.RuntimeAppleVF, func(ctx context.Context, report func(sandbox.PullProgress)) error {
		if _, err := vmkernel.NewStore(kernelDir(cfg)).Fetch(ctx, func(complete, total int64) {
			report(sandbox.PullProgress{Status: "Downloading kernel", Current: complete, Total: total})
		}); err != nil {
			return err
		}

		report(sandbox.PullProgress{Status: "Starting the runtime"})
		entry := newVMRuntime(cfg, roots, store, logger)
		if entry.driver == nil {
			// Something else is still missing, so say which rather than reporting a vague failure.
			return fmt.Errorf("the runtime still needs setup after downloading: %s", entry.missing)
		}

		svc.RegisterRuntime(entry.name, entry.driver)
		set.add(entry)
		svc.MarkRuntimeReady(entry.name)
		return nil
	})
}
