//go:build darwin

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

func TestKernelVersionFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "a local build carries its version", path: "/x/dist/kernel/vmlinux-6.18.15-186",
			want: "6.18.15-186"},
		{name: "a downloaded kernel is named by the folder, not the file", path: "/x/6.18.15-186/vmlinux",
			want: unknownVersion},
		{name: "a file someone pointed us at", path: "/x/my-own-kernel", want: unknownVersion},
		{name: "the prefix alone says nothing", path: "/x/vmlinux-", want: unknownVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kernelVersionFromPath(tc.path); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveKernel(t *testing.T) {
	writeKernel := func(t *testing.T, dir, name string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("KERNEL"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("a configured path wins and is reported as configured", func(t *testing.T) {
		dir := t.TempDir()
		path := writeKernel(t, dir, "my-own-kernel")
		asset, ok := resolveKernel(&config.Config{DataDir: dir, VMKernel: path})
		if !ok {
			t.Fatal("a configured kernel was not used")
		}
		if asset.path != path || asset.source != sourceConfigured {
			t.Errorf("got %q from %q, want %q from %q", asset.path, asset.source, path, sourceConfigured)
		}
	})

	t.Run("a configured path that does not exist is treated as absent", func(t *testing.T) {
		dir := t.TempDir()
		if _, ok := resolveKernel(&config.Config{DataDir: dir, VMKernel: filepath.Join(dir, "typo")}); ok {
			t.Error("a missing kernel was reported as present")
		}
	})

	// The download is what a user gets, so a checkout's own build must be asked for by name.
	t.Run("a downloaded kernel is reported with its release version", func(t *testing.T) {
		dir := t.TempDir()
		want := writeKernel(t, filepath.Join(dir, "vm-kernel", vmruntime.KernelVersion), "vmlinux")
		asset, ok := resolveKernel(&config.Config{DataDir: dir})
		if !ok {
			t.Fatal("a downloaded kernel was not found")
		}
		if asset.path != want {
			t.Errorf("path: got %q, want %q", asset.path, want)
		}
		if asset.source != sourceRelease {
			t.Errorf("source: got %q, want %q", asset.source, sourceRelease)
		}
		if asset.version != vmruntime.KernelVersion {
			t.Errorf("version: got %q, want %q", asset.version, vmruntime.KernelVersion)
		}
	})

	t.Run("nothing on disk means the runtime needs setup", func(t *testing.T) {
		if _, ok := resolveKernel(&config.Config{DataDir: t.TempDir()}); ok {
			t.Error("an empty data dir reported a kernel")
		}
	})
}

func TestResolveGuestInit(t *testing.T) {
	t.Run("a configured init is used as it is", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "my-own-init")
		if err := os.WriteFile(path, []byte("INIT"), 0o755); err != nil {
			t.Fatal(err)
		}
		asset, err := resolveGuestInit(&config.Config{DataDir: dir, VMGuestInit: path})
		if err != nil {
			t.Fatalf("resolveGuestInit: %v", err)
		}
		if asset.path != path || asset.source != sourceConfigured {
			t.Errorf("got %q from %q, want %q from %q", asset.path, asset.source, path, sourceConfigured)
		}
	})

	t.Run("a configured init that is a directory is an error", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := resolveGuestInit(&config.Config{DataDir: dir, VMGuestInit: dir}); err == nil {
			t.Error("expected an error")
		}
	})
}
