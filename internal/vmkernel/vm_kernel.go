// Package vmkernel keeps the Linux kernel that VM sessions boot, downloading it from this build's own
// release and caching it on disk.
//
// Kata Containers builds the kernel and our release carries a repackaged copy, because Kata ships it
// inside a tarball around a gigabyte in size and no user should download that to get 15MB.
//
// Nothing here runs at startup. A node with no kernel reports that it needs setup and stays usable for
// containers, so booting deepcove never waits on a download and never fails because the machine is
// offline.
package vmkernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/waltzofpearls/deepcove/internal/ghrelease"
	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

// kernelFileName is the file inside the release archive.
const kernelFileName = "vmlinux"

// ErrNotFetched means no kernel is on disk yet. The caller reports that the runtime needs setting up
// rather than treating it as a failure.
var ErrNotFetched = errors.New("vmkernel: no kernel has been downloaded yet")

// releaseAssetSource is the part of ghrelease.Client a Downloader uses, so tests can answer it locally.
type releaseAssetSource interface {
	ForVersion(ctx context.Context, version string) (ghrelease.Release, error)
	Download(ctx context.Context, release ghrelease.Release, assetName, dir string,
		report func(complete, total int64)) (string, error)
}

// Downloader puts the guest kernel this build needs on local disk and says where it is.
type Downloader struct {
	// downloadedKernelDir holds one subfolder per kernel version, each holding a single vmlinux file, so
	// a new version lands beside the old one instead of overwriting it.
	downloadedKernelDir string
	kernelVersion       string
	kernelAssetName     string
	// deepcoveVersion picks the release to download from: this build's own, so the kernel and the binary
	// are always the pair that was tested together.
	deepcoveVersion string
	assetSource     releaseAssetSource
}

// NewDownloader keeps kernels under downloadedKernelDir. deepcoveVersion is this build's own version.
func NewDownloader(downloadedKernelDir, deepcoveVersion string) *Downloader {
	return &Downloader{
		downloadedKernelDir: downloadedKernelDir,
		kernelVersion:       vmruntime.KernelVersion,
		kernelAssetName:     vmruntime.GuestKernelAsset(runtime.GOARCH),
		deepcoveVersion:     deepcoveVersion,
		assetSource:         ghrelease.New(),
	}
}

// Path returns the kernel already on disk, without touching the network.
func (d *Downloader) Path() (string, bool) {
	path := d.kernelPath()
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, true
	}
	return "", false
}

// Fetch downloads the kernel unless it is already on disk, and returns its path. Safe to call again.
//
// report, when set, is called with byte counts as the download proceeds, so a caller can show real
// progress rather than a spinner.
func (d *Downloader) Fetch(ctx context.Context, report func(complete, total int64)) (string, error) {
	if path, ok := d.Path(); ok {
		return path, nil
	}
	release, err := d.assetSource.ForVersion(ctx, d.deepcoveVersion)
	if err != nil {
		return "", fmt.Errorf("vmkernel: find the release holding %s: %w", d.kernelAssetName, err)
	}
	if err := os.MkdirAll(d.downloadedKernelDir, 0o755); err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	// Downloading and unpacking inside downloadedKernelDir keeps the last step a rename on one
	// filesystem, so an interrupted fetch never leaves half a kernel where Path would find it.
	workDir, err := os.MkdirTemp(d.downloadedKernelDir, ".download-")
	if err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	archivePath, err := d.assetSource.Download(ctx, release, d.kernelAssetName, workDir, report)
	if err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	unpackedPath, err := ghrelease.ExtractBinary(archivePath, kernelFileName, workDir)
	if err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	if err := os.Chmod(unpackedPath, 0o644); err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	kernelPath := d.kernelPath()
	if err := os.MkdirAll(filepath.Dir(kernelPath), 0o755); err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	if err := os.Rename(unpackedPath, kernelPath); err != nil {
		return "", fmt.Errorf("vmkernel: %w", err)
	}
	return kernelPath, nil
}

func (d *Downloader) kernelPath() string {
	return filepath.Join(d.downloadedKernelDir, d.kernelVersion, kernelFileName)
}
