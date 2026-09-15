// Package vmassets keeps the files VM sessions need that we did not write, downloading them from this
// build's own release and caching them on disk.
//
// Nothing here runs at startup. A node missing an artifact reports that it needs setup and stays usable
// for containers, so booting deepcove never waits on a download and never fails because the machine is
// offline.
package vmassets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/waltzofpearls/deepcove/internal/ghrelease"
)

// ErrNotFetched means the artifact is not on disk yet. The caller reports that the runtime needs setting
// up rather than treating it as a failure.
var ErrNotFetched = errors.New("vmassets: this artifact has not been downloaded yet")

// releaseAssetSource is the part of ghrelease.Client a Store uses, so tests can answer it locally.
type releaseAssetSource interface {
	ForVersion(ctx context.Context, version string) (ghrelease.Release, error)
	Download(ctx context.Context, release ghrelease.Release, assetName, dir string,
		report func(complete, total int64)) (string, error)
}

// Store is the artifacts on this machine: where they are, and how to get the ones that are missing.
type Store struct {
	// dir holds one folder per artifact, each with one folder per version, so a new version lands beside
	// the old one instead of overwriting it.
	dir string
	// deepcoveVersion picks the release to download from: this build's own, so an artifact and the
	// binary that uses it are always the pair that was tested together.
	deepcoveVersion string
	assetSource     releaseAssetSource
}

// New keeps artifacts under dir. deepcoveVersion is this build's own version.
func New(dir, deepcoveVersion string) *Store {
	return &Store{dir: dir, deepcoveVersion: deepcoveVersion, assetSource: ghrelease.New()}
}

// Path returns an artifact already on disk, without touching the network.
func (s *Store) Path(a Artifact) (string, bool) {
	path := s.pathFor(a)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, true
	}
	return "", false
}

// Missing is the artifacts of these that still have to be downloaded.
func (s *Store) Missing(artifacts []Artifact) []Artifact {
	var missing []Artifact
	for _, a := range artifacts {
		if _, ok := s.Path(a); !ok {
			missing = append(missing, a)
		}
	}
	return missing
}

// Fetch downloads an artifact unless it is already on disk, and returns its path. Safe to call again.
//
// report, when set, is called with byte counts as the download proceeds, so a caller can show real
// progress rather than a spinner.
func (s *Store) Fetch(ctx context.Context, a Artifact, report func(complete, total int64)) (string, error) {
	if path, ok := s.Path(a); ok {
		return path, nil
	}
	release, err := s.assetSource.ForVersion(ctx, s.deepcoveVersion)
	if err != nil {
		return "", fmt.Errorf("vmassets: find the release holding %s: %w", a.Asset, err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	// Downloading and unpacking inside dir keeps the last step a rename on one filesystem, so an
	// interrupted fetch never leaves half a file where Path would find it.
	workDir, err := os.MkdirTemp(s.dir, ".download-")
	if err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	archivePath, err := s.assetSource.Download(ctx, release, a.Asset, workDir, report)
	if err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	unpackedPath, err := ghrelease.ExtractBinary(archivePath, a.File, workDir)
	if err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	// Executable, because three of the four are host binaries; a kernel with the bit set is harmless.
	if err := os.Chmod(unpackedPath, 0o755); err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	path := s.pathFor(a)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	if err := os.Rename(unpackedPath, path); err != nil {
		return "", fmt.Errorf("vmassets: %w", err)
	}
	return path, nil
}

func (s *Store) pathFor(a Artifact) string {
	return filepath.Join(s.dir, a.Name, a.Version, a.File)
}
