//go:build darwin

package cli

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// newVMRuntime builds the virtual machine runtime, when this machine can actually provide it. On macOS
// that is Apple VF.
//
// It is deliberately quiet about failure: a node that cannot run VMs still runs sessions in containers,
// which is a working node rather than a broken one. So an unavailable VM runtime is a zero entry, not an
// error, with the reason logged for a user who expected it to be there.
func newVMRuntime(cfg *config.Config, allowedRoots []string, store credbroker.Store, logger *slog.Logger) runtimeEntry {
	kernel := firstExisting(cfg.VMKernel, defaultKernelPaths()...)
	if kernel == "" {
		logger.Info("apple vf runtime unavailable: no guest kernel found",
			"hint", "set DEEPCOVE_VM_KERNEL, or see docs for fetching one")
		return runtimeEntry{}
	}
	guestInit := firstExisting(cfg.VMGuestInit, defaultGuestInitPaths()...)
	if guestInit == "" {
		logger.Info("apple vf runtime unavailable: no guest init binary found",
			"hint", "run `make guest-init`, or set DEEPCOVE_VM_GUEST_INIT")
		return runtimeEntry{}
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

// defaultKernelPaths and defaultGuestInitPaths are where the Makefile writes these artifacts, so a
// developer who has run the build finds the runtime already offered without configuring anything.
func defaultKernelPaths() []string {
	matches, _ := filepath.Glob("dist/kernel/vmlinux-*")
	return matches
}

func defaultGuestInitPaths() []string {
	return []string{"dist/guest/deepcove-guest-init"}
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
