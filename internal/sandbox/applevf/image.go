package applevf

import (
	"archive/tar"
	"context"
	"fmt"
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

// imageStore turns an OCI image reference into a read only ext4 base image, cached by image digest so a
// given image is pulled and flattened at most once per node. Pulling goes straight to the registry (no
// Docker), which is why the VF runtime has no Docker dependency.
//
// resolveDigest and pull are fields rather than direct calls so tests can drive the cache and flatten
// logic with a synthetic image and no network.
type imageStore struct {
	dir      string
	platform v1.Platform
	// inject is baked into every base image built by this store: the guest init binary and its overlay
	// mount point, without which the image cannot boot. The driver populates it once at construction.
	inject        injection
	resolveDigest func(ctx context.Context, ref string) (string, error)
	pull          func(ctx context.Context, ref string, platform v1.Platform) (v1.Image, error)
}

func newImageStore(dir string) *imageStore {
	return &imageStore{
		dir:           dir,
		platform:      guestPlatform(),
		resolveDigest: resolveDigestRemote,
		pull:          pullRemote,
	}
}

// guestPlatform is the OCI platform to pull. Apple VF runs the Linux guest on the host's own CPU with no
// emulation, so the guest architecture is the host's. Without this, go-containerregistry defaults to
// linux/amd64 and an Apple Silicon guest gets x86 binaries that fail to exec (ENOEXEC), panicking the VM.
func guestPlatform() v1.Platform {
	return v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
}

// baseImage returns the path to the cached ext4 base for ref, building it on a cache miss. The digest is
// resolved first (a cheap registry HEAD) so a cache hit avoids pulling any layers. A tag that has moved
// to new content resolves to a new digest and so a new cache entry, which is the point of keying on
// digest rather than tag.
func (s *imageStore) baseImage(ctx context.Context, ref string) (string, error) {
	digest, err := s.resolveDigest(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	path := s.cachePath(digest)
	if _, err := os.Stat(path); err == nil {
		return path, nil // cache hit
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat cached image: %w", err)
	}

	img, err := s.pull(ctx, ref, s.platform)
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	if err := s.buildBase(img, path); err != nil {
		return "", err
	}
	return path, nil
}

// buildBase flattens img into an ext4 image, writing to a temp file and renaming into place so a
// crashed or concurrent build never leaves a half written image at the cache path.
func (s *imageStore) buildBase(img v1.Image, path string) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create image cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, "build-*.ext4")
	if err != nil {
		return fmt.Errorf("create temp image: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // no-op after a successful rename
	}()

	rc := mutate.Extract(img) // flattened rootfs as a single tar stream, whiteouts resolved
	defer func() { _ = rc.Close() }()
	if _, err := writeRootfs(tmp, tar.NewReader(rc), s.inject); err != nil {
		return fmt.Errorf("build rootfs: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flush image: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit image: %w", err)
	}
	return nil
}

// cachePath maps an image digest to its on disk ext4 path. The digest is "sha256:<hex>"; the algorithm
// separator becomes a dash so it is a valid single path segment. The architecture is part of the name
// because a multi arch tag resolves to one index digest across platforms, so two different guest
// architectures would otherwise collide on the same cache entry.
func (s *imageStore) cachePath(digest string) string {
	return filepath.Join(s.dir, strings.ReplaceAll(digest, ":", "-")+"-"+s.platform.Architecture+".ext4")
}

// Anonymous auth: the agent images are public GHCR pulls (see the publishing plan), and it keeps the
// docker/cli credential-file dependency tree out of the build.
func resolveDigestRemote(ctx context.Context, ref string) (string, error) {
	return crane.Digest(ref, crane.WithContext(ctx), crane.WithAuth(authn.Anonymous))
}

func pullRemote(ctx context.Context, ref string, platform v1.Platform) (v1.Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}
	return remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithPlatform(platform),
	)
}

// cached reports whether a base image is already built for ref, without building one. Used by the
// preflight check so the UI can offer to download before a session is created.
func (s *imageStore) cached(ctx context.Context, ref string) (bool, error) {
	digest, err := s.resolveDigest(ctx, ref)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", ref, err)
	}
	if _, err := os.Stat(s.cachePath(digest)); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat cached image: %w", err)
	}
	return false, nil
}
