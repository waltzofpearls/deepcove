package applevf

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// newTestDriver builds a Driver whose image store is fed a synthetic image, so Create can be exercised
// without a network or a VM.
func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	dir := t.TempDir()
	d, err := New(filepath.Join(dir, "vmlinux"), filepath.Join(dir, "init"), dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	img := layerImage(t, map[string]string{"etc/hostname": "deepcove\n"})
	d.images = &imageStore{
		dir:           dir,
		platform:      guestPlatform(),
		resolveDigest: func(context.Context, string) (string, error) { return "sha256:test", nil },
		pull:          func(context.Context, string, v1.Platform) (v1.Image, error) { return img, nil },
	}
	return d
}

func TestNewRejectsMissingPaths(t *testing.T) {
	cases := []struct {
		name      string
		kernel    string
		guestInit string
	}{
		{"no kernel", "", "/init"},
		{"no guest init", "/vmlinux", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.kernel, tc.guestInit, t.TempDir()); err == nil {
				t.Error("expected an error for an incomplete driver configuration")
			}
		})
	}
}

func TestCreateBuildsTheBaseImageAndReturnsAHandle(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "ghcr.io/x/agent:latest"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h == "" {
		t.Error("Create must return a non empty handle")
	}
	s, err := d.lookup(h)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	assertExt4(t, s.baseImage)
}

func TestCreateRejectsASpecWithNoImage(t *testing.T) {
	d := newTestDriver(t)
	if _, err := d.Create(context.Background(), sandbox.SandboxSpec{}); err == nil {
		t.Error("a spec without an image must be rejected")
	}
}

func TestHandlesAreUnique(t *testing.T) {
	d := newTestDriver(t)
	spec := sandbox.SandboxSpec{Image: "ghcr.io/x/agent:latest"}
	first, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if first == second {
		t.Error("each sandbox must get its own handle, even for the same image")
	}
}

// Operations on a handle that was never created must say so rather than panic on a nil lookup.
func TestUnknownHandleIsNotFound(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	const ghost = sandbox.Handle("applevf-does-not-exist")

	if err := d.Start(ctx, ghost); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Start error = %v, want ErrNotFound", err)
	}
	if err := d.Stop(ctx, ghost); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Stop error = %v, want ErrNotFound", err)
	}
	if _, err := d.Exec(ctx, ghost, sandbox.ExecSpec{Cmd: []string{"/bin/true"}}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Exec error = %v, want ErrNotFound", err)
	}
}

// Exec against a created but unstarted sandbox is a lifecycle conflict, not a missing sandbox.
func TestExecBeforeStartIsAConflict(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := d.Exec(context.Background(), h, sandbox.ExecSpec{Cmd: []string{"/bin/true"}}); !errors.Is(err, sandbox.ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

// Stopping a sandbox that never started is a no op, so session teardown does not have to track state.
func TestStopWithoutStartIsANoOp(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Stop(context.Background(), h); err != nil {
		t.Errorf("Stop on an unstarted sandbox = %v, want nil", err)
	}
}

func TestDestroyForgetsTheSandbox(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Destroy(context.Background(), h); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := d.lookup(h); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("the sandbox should be gone after Destroy, got %v", err)
	}
}

// A VM needs concrete CPU and memory values, unlike a container that can inherit the host's, so an unset
// limit has to become a sensible default rather than zero.
func TestResourceDefaults(t *testing.T) {
	if got := cpuCount(sandbox.Resources{}); got != defaultCPUCount {
		t.Errorf("cpuCount(zero) = %d, want %d", got, defaultCPUCount)
	}
	if got := memoryBytes(sandbox.Resources{}); got != defaultMemoryBytes {
		t.Errorf("memoryBytes(zero) = %d, want %d", got, defaultMemoryBytes)
	}
	if got := cpuCount(sandbox.Resources{CPU: 4}); got != 4 {
		t.Errorf("cpuCount(4) = %d, want 4", got)
	}
	if got := memoryBytes(sandbox.Resources{Memory: 512 << 20}); got != 512<<20 {
		t.Errorf("memoryBytes(512MiB) = %d", got)
	}
}

// The unimplemented optional operations must report ErrNotSupported so the session layer can treat them
// as a capability signal rather than a failure.
func TestUnsupportedOperations(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	if _, err := d.Snapshot(ctx, "h"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Snapshot = %v, want ErrNotSupported", err)
	}
	if err := d.Restore(ctx, "h", "snap"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Restore = %v, want ErrNotSupported", err)
	}
	if err := d.RemoveStorage(ctx, "vol"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("RemoveStorage = %v, want ErrNotSupported", err)
	}
	if err := d.CopyIn(ctx, "h", "/a", "/b"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("CopyIn = %v, want ErrNotSupported", err)
	}
}

func TestImageExistsReflectsTheCache(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	exists, err := d.ImageExists(ctx, "img:tag")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if exists {
		t.Error("nothing is cached before the first build")
	}

	if err := d.ImagePull(ctx, "img:tag", nil); err != nil {
		t.Fatalf("ImagePull: %v", err)
	}
	exists, err = d.ImageExists(ctx, "img:tag")
	if err != nil {
		t.Fatalf("ImageExists after pull: %v", err)
	}
	if !exists {
		t.Error("the image should be cached after a pull")
	}
}
