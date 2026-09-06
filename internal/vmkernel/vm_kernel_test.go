package vmkernel

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

func downloaderFor(t *testing.T, files map[string]string) (*Downloader, *fakeAssetSource) {
	t.Helper()
	source := &fakeAssetSource{files: files}
	d := NewDownloader(t.TempDir(), "v1.2.3")
	d.assetSource = source
	return d, source
}

func TestFetch(t *testing.T) {
	t.Run("writes the kernel out of the archive", func(t *testing.T) {
		d, _ := downloaderFor(t, map[string]string{kernelFileName: "KERNEL"})
		path, err := d.Fetch(context.Background(), nil)
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
		// The version is in the path, which is what keeps two kernels from colliding.
		if !strings.Contains(path, d.kernelVersion) {
			t.Errorf("path %q does not carry the kernel version", path)
		}
	})

	t.Run("an archive without a kernel is an error, not an empty file", func(t *testing.T) {
		d, _ := downloaderFor(t, map[string]string{"README": "nope"})
		if _, err := d.Fetch(context.Background(), nil); err == nil {
			t.Fatal("expected an error")
		}
		if path, ok := d.Path(); ok {
			t.Errorf("got %q, want nothing left on disk", path)
		}
	})

	t.Run("a second fetch does not download again", func(t *testing.T) {
		d, source := downloaderFor(t, map[string]string{kernelFileName: "KERNEL"})
		if _, err := d.Fetch(context.Background(), nil); err != nil {
			t.Fatalf("first fetch: %v", err)
		}
		if _, err := d.Fetch(context.Background(), nil); err != nil {
			t.Fatalf("second fetch: %v", err)
		}
		if source.downloads != 1 {
			t.Errorf("downloads = %d, want 1", source.downloads)
		}
	})

	// A new version must land in its own entry rather than reusing the previous kernel.
	t.Run("a new kernel version is downloaded into its own entry", func(t *testing.T) {
		d, source := downloaderFor(t, map[string]string{kernelFileName: "OLD"})
		oldPath, err := d.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("first fetch: %v", err)
		}

		d.kernelVersion = "9.9.9-1"
		source.files = map[string]string{kernelFileName: "NEW"}
		newPath, err := d.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("second fetch: %v", err)
		}
		if newPath == oldPath {
			t.Fatal("a different version reused the same path")
		}
		body, _ := os.ReadFile(newPath)
		if string(body) != "NEW" {
			t.Errorf("content: got %q, want NEW", body)
		}
	})

	t.Run("a failed download leaves nothing behind", func(t *testing.T) {
		d, source := downloaderFor(t, map[string]string{kernelFileName: "KERNEL"})
		source.err = errors.New("offline")
		if _, err := d.Fetch(context.Background(), nil); err == nil {
			t.Fatal("expected an error")
		}
		if path, ok := d.Path(); ok {
			t.Errorf("got %q, want nothing left on disk", path)
		}
	})
}

func TestPath(t *testing.T) {
	t.Run("reports nothing before anything is downloaded", func(t *testing.T) {
		d := NewDownloader(t.TempDir(), "v1.2.3")
		if path, ok := d.Path(); ok {
			t.Errorf("got %q, want no kernel", path)
		}
	})

	// Startup calls this, so it must answer from disk alone.
	t.Run("finds a downloaded kernel without the network", func(t *testing.T) {
		d, _ := downloaderFor(t, map[string]string{kernelFileName: "KERNEL"})
		want, err := d.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}

		offline := NewDownloader(d.downloadedKernelDir, "v1.2.3")
		offline.assetSource = &fakeAssetSource{err: errors.New("network used")}
		got, ok := offline.Path()
		if !ok {
			t.Fatal("a downloaded kernel was not found on disk")
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ignores an empty file left by an interrupted write", func(t *testing.T) {
		dir := t.TempDir()
		d := NewDownloader(dir, "v1.2.3")
		if err := os.MkdirAll(filepath.Join(dir, d.kernelVersion), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.kernelPath(), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if path, ok := d.Path(); ok {
			t.Errorf("got %q, want an empty kernel to be ignored", path)
		}
	})
}
