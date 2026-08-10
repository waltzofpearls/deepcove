// Package vmkernel keeps the Linux kernel that VM sessions boot, fetching it from a registry and
// caching it on disk.
//
// The kernel is distributed as a single file OCI image rather than a plain download, so it reuses the
// registry client, the platform selection and the digest addressing that pulling session images already
// needs. Kata Containers builds the kernel; that image is only a repackaging, because Kata ships it
// inside a tarball around a gigabyte in size and no user should download that to get 15MB.
//
// Nothing here runs at startup. A node with no kernel reports that it needs setup and stays usable for
// containers, so booting deepcove never waits on a download and never fails because the machine is
// offline.
package vmkernel

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Ref is the kernel this build boots guests with, pinned to an exact tag. A version of deepcove always
// wants the kernel it was tested against, so bumping it is a code change rather than something that can
// happen underneath a user.
const Ref = "ghcr.io/waltzofpearls/deepcove-kernel:6.18.15-186"

// kernelName is the file inside the image, written by build/kernel.Dockerfile.
const kernelName = "vmlinux"

// ErrNotFetched means no kernel is on disk yet. The caller reports that the runtime needs setting up
// rather than treating it as a failure.
var ErrNotFetched = errors.New("vmkernel: no kernel has been downloaded yet")

// Store caches one kernel per image digest under dir. Keying on digest means a moved tag lands in its
// own entry instead of quietly reusing the previous kernel.
type Store struct {
	dir      string
	ref      string
	platform v1.Platform

	// Seams for tests, so the cache and extraction logic can run against a synthetic image offline.
	resolveDigest func(ctx context.Context, ref string) (string, error)
	pull          func(ctx context.Context, ref string, platform v1.Platform) (v1.Image, error)
}

func NewStore(dir string) *Store {
	return &Store{
		dir:           dir,
		ref:           Ref,
		platform:      v1.Platform{OS: "linux", Architecture: runtime.GOARCH},
		resolveDigest: resolveDigestRemote,
		pull:          pullRemote,
	}
}

// Cached returns the kernel already on disk, without touching the network. The digest is part of the
// path, so this reads a directory rather than asking a registry what the tag points at now.
func (s *Store) Cached() (string, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(s.dir, e.Name(), kernelName)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, true
		}
	}
	return "", false
}

// Fetch downloads the kernel unless it is already cached, and returns its path. Safe to call again: a
// cache hit costs one registry HEAD, and an already extracted kernel is not rewritten.
//
// report, when set, is called with byte counts as the download proceeds. The registry reports real
// totals, so a caller can show actual progress rather than a spinner.
func (s *Store) Fetch(ctx context.Context, report func(complete, total int64)) (string, error) {
	digest, err := s.resolveDigest(ctx, s.ref)
	if err != nil {
		return "", fmt.Errorf("vmkernel: resolve %s: %w", s.ref, err)
	}

	path := filepath.Join(s.dir, digestDir(digest), kernelName)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}

	img, err := s.pull(ctx, s.ref, s.platform)
	if err != nil {
		return "", fmt.Errorf("vmkernel: pull %s: %w", s.ref, err)
	}
	if err := extractTo(img, path, report); err != nil {
		return "", err
	}
	return path, nil
}

// extractTo writes the kernel out of the image. The image holds exactly one file, but it is read as a
// flattened filesystem anyway so a future image with more in it still works.
//
// Progress is counted here rather than through remote.WithProgress: that option only closes its channel
// on the write paths, so a pull would leave a reader waiting forever. The layers stream in as this
// copies, so bytes copied is a fair measure of the download, and the tar header gives the total.
func extractTo(img v1.Image, path string, report func(complete, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("vmkernel: %w", err)
	}

	rc := mutate.Extract(img)
	defer func() { _ = rc.Close() }()

	tr := tar.NewReader(rc)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("vmkernel: read image: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != kernelName {
			continue
		}
		var src io.Reader = tr
		if report != nil {
			src = &countingReader{r: tr, total: header.Size, report: report}
		}
		return writeFile(path, src)
	}
	return fmt.Errorf("vmkernel: image contains no %s", kernelName)
}

// writeFile writes to a temporary name and renames, so an interrupted download cannot leave a truncated
// kernel that the next boot would hand to the hypervisor.
func writeFile(path string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), kernelName+".*")
	if err != nil {
		return fmt.Errorf("vmkernel: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, r); err != nil { //nolint:gosec // the source is our own pinned image
		_ = tmp.Close()
		return fmt.Errorf("vmkernel: write kernel: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vmkernel: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("vmkernel: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("vmkernel: %w", err)
	}
	return nil
}

// countingReader reports how much has been read, so the caller can show real progress.
type countingReader struct {
	r        io.Reader
	complete int64
	total    int64
	report   func(complete, total int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.complete += int64(n)
		c.report(c.complete, c.total)
	}
	return n, err
}

// digestDir turns "sha256:abc..." into a directory name, since a colon is awkward in a path.
func digestDir(digest string) string {
	return strings.ReplaceAll(digest, ":", "-")
}

func resolveDigestRemote(ctx context.Context, ref string) (string, error) {
	return crane.Digest(ref, crane.WithContext(ctx), crane.WithAuth(authn.Anonymous))
}

func pullRemote(ctx context.Context, ref string, platform v1.Platform) (v1.Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("vmkernel: parse reference: %w", err)
	}
	return remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithPlatform(platform),
	)
}
