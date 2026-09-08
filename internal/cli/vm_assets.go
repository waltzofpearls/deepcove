package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/guestasset"
	"github.com/waltzofpearls/deepcove/internal/vmassets"
)

// assetSource says where a VM artifact came from. Logged at startup, so "which kernel is this booting"
// has an answer.
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

// resolveKernel finds the kernel to boot guests with, without downloading. A node with no kernel says it
// needs setup, so startup never waits on the network. A local build is used only when asked for.
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
	return downloadedAsset(cfg, vmassets.GuestKernel(runtime.GOARCH))
}

// downloadedAsset is an artifact this node already fetched from its own release.
func downloadedAsset(cfg *config.Config, artifact vmassets.Artifact) (vmAsset, bool) {
	path, ok := vmassets.New(artifactDir(cfg), Version).Path(artifact)
	if !ok {
		return vmAsset{}, false
	}
	return vmAsset{
		name: artifact.Name, version: artifact.Version,
		source: sourceRelease, path: path,
	}, true
}

// artifactDir is where downloaded VM artifacts live.
func artifactDir(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "vm-artifacts")
}

// localKernelPaths finds a kernel built in this checkout. Next to the binary, not the working directory,
// because a service starts wherever launchd or systemd puts it.
func localKernelPaths() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), "dist", "kernel", "vmlinux-*"))
	return matches
}

// kernelVersionFromPath reads the version out of a vmlinux-<version> file name.
func kernelVersionFromPath(path string) string {
	if version, ok := strings.CutPrefix(filepath.Base(path), "vmlinux-"); ok && version != "" {
		return version
	}
	return unknownVersion
}

// resolveGuestInit returns the guest init to boot sessions with: a configured one, else the one inside
// this binary.
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
	return vmAsset{name: guestInitAssetName, version: Version, source: sourceEmbedded, path: path}, nil
}

// existingFile returns the first path that is a file. A path that is not there counts as absent, so a
// typo reads as "needs setup" rather than failing later.
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

// logVMAssets records what a VM runtime is about to boot with.
func logVMAssets(logger *slog.Logger, assets ...vmAsset) {
	for _, asset := range assets {
		logger.Info("vm artifact", "name", asset.name, "version", asset.version,
			"source", string(asset.source), "path", asset.path)
	}
}
