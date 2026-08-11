package vmkernel

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// imageWith builds a single layer image holding the given files, so these tests never touch a registry.
func imageWith(t *testing.T, files map[string]string) v1.Image {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: name, Size: int64(len(body)), Mode: 0o644,
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("layer: %v", err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return img
}

// storeFor wires a store whose registry calls are answered locally.
func storeFor(t *testing.T, digest string, img v1.Image) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	s.resolveDigest = func(context.Context, string) (string, error) { return digest, nil }
	s.pull = func(context.Context, string, v1.Platform) (v1.Image, error) { return img, nil }
	return s
}

func TestFetch(t *testing.T) {
	t.Run("writes the kernel out of the image", func(t *testing.T) {
		s := storeFor(t, "sha256:aaa", imageWith(t, map[string]string{"vmlinux": "KERNEL"}))
		path, err := s.Fetch(context.Background(), nil)
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
		// The digest is in the path, which is what keeps two kernels from colliding.
		if !strings.Contains(path, "sha256-aaa") {
			t.Errorf("path %q does not carry the digest", path)
		}
	})

	t.Run("an image without a kernel is an error, not an empty file", func(t *testing.T) {
		s := storeFor(t, "sha256:bbb", imageWith(t, map[string]string{"README": "nope"}))
		if _, err := s.Fetch(context.Background(), nil); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a second fetch does not pull again", func(t *testing.T) {
		s := storeFor(t, "sha256:ccc", imageWith(t, map[string]string{"vmlinux": "KERNEL"}))
		if _, err := s.Fetch(context.Background(), nil); err != nil {
			t.Fatalf("first fetch: %v", err)
		}
		s.pull = func(context.Context, string, v1.Platform) (v1.Image, error) {
			return nil, errors.New("pulled again")
		}
		if _, err := s.Fetch(context.Background(), nil); err != nil {
			t.Fatalf("second fetch pulled again: %v", err)
		}
	})

	// A moved tag must land in its own entry rather than reusing the previous kernel.
	t.Run("a new digest is fetched into its own entry", func(t *testing.T) {
		s := storeFor(t, "sha256:ddd", imageWith(t, map[string]string{"vmlinux": "OLD"}))
		oldPath, err := s.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("first fetch: %v", err)
		}

		s.resolveDigest = func(context.Context, string) (string, error) { return "sha256:eee", nil }
		s.pull = func(context.Context, string, v1.Platform) (v1.Image, error) {
			return imageWith(t, map[string]string{"vmlinux": "NEW"}), nil
		}
		newPath, err := s.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("second fetch: %v", err)
		}
		if newPath == oldPath {
			t.Fatal("a different digest reused the same path")
		}
		body, _ := os.ReadFile(newPath)
		if string(body) != "NEW" {
			t.Errorf("content: got %q, want NEW", body)
		}
	})
}

func TestCached(t *testing.T) {
	t.Run("reports nothing before anything is fetched", func(t *testing.T) {
		s := NewStore(t.TempDir())
		if path, ok := s.Cached(); ok {
			t.Errorf("got %q, want no kernel", path)
		}
	})

	// Startup calls this, so it must answer from disk alone.
	t.Run("finds a fetched kernel without the network", func(t *testing.T) {
		s := storeFor(t, "sha256:fff", imageWith(t, map[string]string{"vmlinux": "KERNEL"}))
		want, err := s.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}

		offline := NewStore(s.dir)
		offline.resolveDigest = func(context.Context, string) (string, error) {
			return "", errors.New("network used")
		}
		got, ok := offline.Cached()
		if !ok {
			t.Fatal("a fetched kernel was not found on disk")
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ignores an empty file left by an interrupted write", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "sha256-partial"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sha256-partial", kernelName), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if path, ok := NewStore(dir).Cached(); ok {
			t.Errorf("got %q, want an empty kernel to be ignored", path)
		}
	})
}
