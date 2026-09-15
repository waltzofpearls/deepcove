package vmassets

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/ghrelease"
	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

// fakeAssetSource hands back an archive holding the given files, so these tests never use the network.
type fakeAssetSource struct {
	files     map[string]string
	downloads int
	err       error
}

func (f *fakeAssetSource) ForVersion(context.Context, string) (ghrelease.Release, error) {
	if f.err != nil {
		return ghrelease.Release{}, f.err
	}
	return ghrelease.Release{Tag: "v1.2.3"}, nil
}

func (f *fakeAssetSource) Download(_ context.Context, _ ghrelease.Release, assetName, dir string,
	report func(complete, total int64)) (string, error) {
	f.downloads++
	if f.err != nil {
		return "", f.err
	}
	path := filepath.Join(dir, assetName)
	if err := writeTarGz(path, f.files); err != nil {
		return "", err
	}
	if report != nil {
		report(1, 1)
	}
	return path, nil
}

func writeTarGz(path string, files map[string]string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	zw := gzip.NewWriter(file)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Size: int64(len(body)), Mode: 0o644,
		}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func storeFor(t *testing.T, files map[string]string) (*Store, *fakeAssetSource) {
	t.Helper()
	source := &fakeAssetSource{files: files}
	s := New(t.TempDir(), "v1.2.3")
	s.assetSource = source
	return s, source
}

func kernel() Artifact {
	return GuestKernel("arm64")
}

func TestFetch(t *testing.T) {
	t.Run("writes the file out of the archive", func(t *testing.T) {
		s, _ := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
		path, err := s.Fetch(context.Background(), kernel(), nil)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(body) != "KERNEL" {
			t.Errorf("content: got %q, want KERNEL", body)
		}
		// The name and the version are both in the path, so two artifacts never collide.
		if !strings.Contains(path, "guest-kernel") || !strings.Contains(path, vmruntime.KernelVersion) {
			t.Errorf("path %q should carry the name and the version", path)
		}
	})

	t.Run("an archive without the file is an error, not an empty file", func(t *testing.T) {
		s, _ := storeFor(t, map[string]string{"README": "nope"})
		if _, err := s.Fetch(context.Background(), kernel(), nil); err == nil {
			t.Fatal("expected an error")
		}
		if path, ok := s.Path(kernel()); ok {
			t.Errorf("got %q, want nothing left on disk", path)
		}
	})

	t.Run("a second fetch does not download again", func(t *testing.T) {
		s, source := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
		for range 2 {
			if _, err := s.Fetch(context.Background(), kernel(), nil); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
		}
		if source.downloads != 1 {
			t.Errorf("downloads = %d, want 1", source.downloads)
		}
	})

	// Each artifact has its own folder, so downloading one never disturbs another.
	t.Run("artifacts do not collide", func(t *testing.T) {
		s, source := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
		kernelPath, err := s.Fetch(context.Background(), kernel(), nil)
		if err != nil {
			t.Fatalf("Fetch the kernel: %v", err)
		}

		passt := LinuxHost("arm64")[2]
		source.files = map[string]string{"passt": "PASST"}
		passtPath, err := s.Fetch(context.Background(), passt, nil)
		if err != nil {
			t.Fatalf("Fetch passt: %v", err)
		}
		if passtPath == kernelPath {
			t.Fatal("two artifacts share one path")
		}
		body, _ := os.ReadFile(kernelPath)
		if string(body) != "KERNEL" {
			t.Errorf("the kernel was overwritten: %q", body)
		}
	})

	t.Run("a failed download leaves nothing behind", func(t *testing.T) {
		s, source := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
		source.err = errors.New("offline")
		if _, err := s.Fetch(context.Background(), kernel(), nil); err == nil {
			t.Fatal("expected an error")
		}
		if path, ok := s.Path(kernel()); ok {
			t.Errorf("got %q, want nothing left on disk", path)
		}
	})
}

func TestPath(t *testing.T) {
	t.Run("reports nothing before anything is downloaded", func(t *testing.T) {
		s := New(t.TempDir(), "v1.2.3")
		if path, ok := s.Path(kernel()); ok {
			t.Errorf("got %q, want nothing", path)
		}
	})

	// Startup calls this, so it must answer from disk alone.
	t.Run("finds a downloaded file without the network", func(t *testing.T) {
		s, _ := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
		want, err := s.Fetch(context.Background(), kernel(), nil)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}

		offline := New(s.dir, "v1.2.3")
		offline.assetSource = &fakeAssetSource{err: errors.New("network used")}
		got, ok := offline.Path(kernel())
		if !ok {
			t.Fatal("a downloaded file was not found on disk")
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ignores an empty file left by an interrupted write", func(t *testing.T) {
		s := New(t.TempDir(), "v1.2.3")
		path := s.pathFor(kernel())
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if path, ok := s.Path(kernel()); ok {
			t.Errorf("got %q, want an empty file to be ignored", path)
		}
	})
}

// The setup button asks what is missing, and a node with nothing needs all of them.
func TestMissing(t *testing.T) {
	s, _ := storeFor(t, map[string]string{"vmlinux": "KERNEL"})
	wanted := append([]Artifact{kernel()}, LinuxHost("arm64")...)

	if got := s.Missing(wanted); len(got) != 4 {
		t.Fatalf("got %d missing, want 4", len(got))
	}
	if _, err := s.Fetch(context.Background(), kernel(), nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got := s.Missing(wanted)
	if len(got) != 3 {
		t.Fatalf("got %d missing after fetching one, want 3", len(got))
	}
	for _, a := range got {
		if a.Name == "guest-kernel" {
			t.Error("the kernel is on disk but was reported missing")
		}
	}
}
