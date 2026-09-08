package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// layerImage builds a synthetic single layer OCI image whose rootfs contains the given files, so the
// pull and flatten pipeline is exercised without a network.
func layerImage(t *testing.T, files map[string]string) v1.Image {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644,
			Size: int64(len(body)), ModTime: time.Unix(1700000000, 0),
		}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close layer tar: %v", err)
	}
	raw := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	})
	if err != nil {
		t.Fatalf("layer from opener: %v", err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("append layer: %v", err)
	}
	return img
}

func TestBaseImageBuildsAndCaches(t *testing.T) {
	dir := t.TempDir()
	img := layerImage(t, map[string]string{"etc/hostname": "deepcove\n", "bin/agent": "#!/bin/sh\n"})

	pulls := 0
	store := &imageStore{
		dir:           dir,
		platform:      v1.Platform{OS: "linux", Architecture: "arm64"},
		resolveDigest: func(context.Context, string) (string, error) { return "sha256:cafe", nil },
		pull: func(context.Context, string, v1.Platform) (v1.Image, error) {
			pulls++
			return img, nil
		},
	}

	path, err := store.baseImage(context.Background(), "ghcr.io/x/agent:latest")
	if err != nil {
		t.Fatalf("baseImage: %v", err)
	}
	if want := filepath.Join(dir, "sha256-cafe-arm64.ext4"); path != want {
		t.Errorf("cache path = %q, want %q", path, want)
	}
	assertExt4(t, path)

	// Second call for the same digest must hit the cache and not pull again.
	if _, err := store.baseImage(context.Background(), "ghcr.io/x/agent:latest"); err != nil {
		t.Fatalf("second baseImage: %v", err)
	}
	if pulls != 1 {
		t.Errorf("pull count = %d, want 1 (second call should hit cache)", pulls)
	}
}

func TestBaseImageNewDigestRebuilds(t *testing.T) {
	dir := t.TempDir()
	img := layerImage(t, map[string]string{"etc/os-release": "deepcove\n"})

	digest := "sha256:one"
	pulls := 0
	store := &imageStore{
		dir:           dir,
		platform:      guestPlatform(),
		resolveDigest: func(context.Context, string) (string, error) { return digest, nil },
		pull: func(context.Context, string, v1.Platform) (v1.Image, error) {
			pulls++
			return img, nil
		},
	}

	if _, err := store.baseImage(context.Background(), "img:tag"); err != nil {
		t.Fatalf("first: %v", err)
	}
	digest = "sha256:two" // tag moved to new content
	if _, err := store.baseImage(context.Background(), "img:tag"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if pulls != 2 {
		t.Errorf("pull count = %d, want 2 (moved tag = new digest = rebuild)", pulls)
	}
}

func TestNewImageStoreWiresRemoteDefaults(t *testing.T) {
	store, err := newImageStore(t.TempDir(), injection{})
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if store.resolveDigest == nil || store.pull == nil {
		t.Fatal("newImageStore must wire the remote resolveDigest and pull defaults")
	}
	if store.platform.Architecture == "" || store.platform.OS != "linux" {
		t.Errorf("platform = %+v, want linux with a concrete architecture", store.platform)
	}
}

// Regression: without an explicit platform, go-containerregistry pulls linux/amd64, so an arm64 guest
// gets x86 binaries and panics with ENOEXEC on the first exec. The store must pass its guest platform
// down to the pull.
func TestBaseImagePullsGuestPlatform(t *testing.T) {
	img := layerImage(t, map[string]string{"bin/agent": "x"})
	want := v1.Platform{OS: "linux", Architecture: "arm64"}

	var got v1.Platform
	store := &imageStore{
		dir:           t.TempDir(),
		platform:      want,
		resolveDigest: func(context.Context, string) (string, error) { return "sha256:beef", nil },
		pull: func(_ context.Context, _ string, p v1.Platform) (v1.Image, error) {
			got = p
			return img, nil
		},
	}
	if _, err := store.baseImage(context.Background(), "img:tag"); err != nil {
		t.Fatalf("baseImage: %v", err)
	}
	if got.OS != want.OS || got.Architecture != want.Architecture {
		t.Errorf("pull platform = %s/%s, want %s/%s", got.OS, got.Architecture, want.OS, want.Architecture)
	}
}

// The same digest built for two architectures must not collide in the cache: a multi arch tag resolves
// to one index digest regardless of platform.
func TestCachePathIncludesArchitecture(t *testing.T) {
	arm := (&imageStore{dir: "/c", platform: v1.Platform{Architecture: "arm64"}}).cachePath("sha256:abc")
	amd := (&imageStore{dir: "/c", platform: v1.Platform{Architecture: "amd64"}}).cachePath("sha256:abc")
	if arm == amd {
		t.Errorf("cache paths collide across architectures: %q", arm)
	}
}

func TestGuestPlatformIsHostArch(t *testing.T) {
	p := guestPlatform()
	if p.OS != "linux" {
		t.Errorf("OS = %q, want linux (the guest is always Linux)", p.OS)
	}
	if p.Architecture != runtime.GOARCH {
		t.Errorf("Architecture = %q, want %q (VF runs the guest on the host CPU)", p.Architecture, runtime.GOARCH)
	}
}

func TestBaseImageResolveError(t *testing.T) {
	store := &imageStore{
		dir:           t.TempDir(),
		platform:      guestPlatform(),
		resolveDigest: func(context.Context, string) (string, error) { return "", errors.New("no such image") },
		pull: func(context.Context, string, v1.Platform) (v1.Image, error) {
			t.Fatal("must not pull when resolve fails")
			return nil, nil
		},
	}
	if _, err := store.baseImage(context.Background(), "img:tag"); err == nil {
		t.Error("expected error when digest resolution fails")
	}
}

func assertExt4(t *testing.T, path string) {
	t.Helper()
	img, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	const magicOffset = 0x438
	if len(img) < magicOffset+2 || img[magicOffset] != 0x53 || img[magicOffset+1] != 0xEF {
		t.Fatalf("not an ext4 image (magic missing) at %s", path)
	}
}

// The cache key must cover the guest init's bytes. Keying only on the OCI digest meant a rebuilt init was
// never picked up: the image holding the OLD one stayed a cache hit, so sessions silently booted stale
// init code. That is how credential persistence appeared broken long after it was fixed.
func TestCachePathChangesWithTheInjectedInit(t *testing.T) {
	dir := t.TempDir()
	initV1 := filepath.Join(dir, "init-v1")
	if err := os.WriteFile(initV1, []byte("guest init version one"), 0o755); err != nil {
		t.Fatal(err)
	}
	storeV1, err := newImageStore(dir, baseInjection(initV1))
	if err != nil {
		t.Fatalf("newImageStore v1: %v", err)
	}

	const digest = "sha256:abc123"
	pathV1 := storeV1.cachePath(digest)

	// Rebuild the init with different content at the SAME path, exactly what `make guest-init` does.
	if err := os.WriteFile(initV1, []byte("guest init version TWO, now with a watcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	storeV2, err := newImageStore(dir, baseInjection(initV1))
	if err != nil {
		t.Fatalf("newImageStore v2: %v", err)
	}
	pathV2 := storeV2.cachePath(digest)

	if pathV1 == pathV2 {
		t.Errorf("a rebuilt guest init reused the cache path %q; it must produce a new one", pathV1)
	}
	// Identical content must still hit the same path, or every restart would rebuild every image.
	storeAgain, err := newImageStore(dir, baseInjection(initV1))
	if err != nil {
		t.Fatalf("newImageStore again: %v", err)
	}
	if got := storeAgain.cachePath(digest); got != pathV2 {
		t.Errorf("unchanged init produced %q, want the stable %q", got, pathV2)
	}
}

// A stale variant is a few hundred MB of ext4, so rebuilds must not accumulate them.
func TestPruneStaleVariantsKeepsOnlyTheCurrentBuild(t *testing.T) {
	dir := t.TempDir()
	initPath := filepath.Join(dir, "init")
	if err := os.WriteFile(initPath, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := newImageStore(dir, baseInjection(initPath))
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}

	const digest = "sha256:abc123"
	current := store.cachePath(digest)
	stale := filepath.Join(dir, store.cachePrefix(digest)+"-initdeadbeef0000.ext4")
	other := filepath.Join(dir, "sha256-somethingelse-"+store.platform.Architecture+"-initcafe.ext4")
	for _, f := range []string{current, stale, other} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	store.pruneStaleVariants(digest)

	if _, err := os.Stat(current); err != nil {
		t.Errorf("the current image was deleted: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale variant of the same image survived pruning")
	}
	// A different image's cache must be untouched: it is still valid for its own sessions.
	if _, err := os.Stat(other); err != nil {
		t.Errorf("pruning removed an unrelated image: %v", err)
	}
}

func TestSweepUnused(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		age     time.Duration
		removed bool
	}{
		{name: "booted today", age: 0},
		{name: "booted last week", age: 7 * 24 * time.Hour},
		{name: "not booted in two months", age: 60 * 24 * time.Hour, removed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sha256-abc-arm64.ext4")
			if err := os.WriteFile(path, []byte("BASE"), 0o600); err != nil {
				t.Fatal(err)
			}
			used := now.Add(-tc.age)
			if err := os.Chtimes(path, used, used); err != nil {
				t.Fatal(err)
			}

			store := &imageStore{dir: dir}
			got := store.sweepUnused(now)

			_, err := os.Stat(path)
			gone := os.IsNotExist(err)
			if gone != tc.removed {
				t.Errorf("removed = %v, want %v", gone, tc.removed)
			}
			if want := map[bool]int{true: 1, false: 0}[tc.removed]; got != want {
				t.Errorf("count = %d, want %d", got, want)
			}
		})
	}
}

// A cache hit has to count as use, or a base in daily service is swept for being old.
func TestCacheHitMarksTheImageUsed(t *testing.T) {
	dir := t.TempDir()
	store := &imageStore{dir: dir, platform: guestPlatform()}
	path := store.cachePath("sha256:abc")
	if err := os.WriteFile(path, []byte("BASE"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	store.markUsed(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("last used %v, want now", info.ModTime())
	}
	if removed := store.sweepUnused(time.Now()); removed != 0 {
		t.Errorf("swept %d images, want none", removed)
	}
}
