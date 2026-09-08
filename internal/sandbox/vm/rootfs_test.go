package vm

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/guestinit"
)

type tarEntry struct {
	hdr  tar.Header
	body string
}

func buildTar(t *testing.T, entries []tarEntry) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		hdr.Size = int64(len(e.body))
		if hdr.ModTime.IsZero() {
			hdr.ModTime = time.Unix(1700000000, 0)
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write header %s: %v", hdr.Name, err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %s: %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return tar.NewReader(&buf)
}

func TestWriteRootfsStats(t *testing.T) {
	entries := []tarEntry{
		{hdr: tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "usr/bin/agent", Typeflag: tar.TypeReg, Mode: 0o755}, body: "#!/bin/sh\n"},
		{hdr: tar.Header{Name: "usr/bin/agent-link", Typeflag: tar.TypeLink, Linkname: "usr/bin/agent"}},
		{hdr: tar.Header{Name: "usr/bin/agent-sym", Typeflag: tar.TypeSymlink, Linkname: "agent"}},
		{hdr: tar.Header{Name: "dev/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3}},
		{hdr: tar.Header{Name: "dev/loop0", Typeflag: tar.TypeBlock, Mode: 0o660, Devmajor: 7, Devminor: 0}},
	}

	imagePath := filepath.Join(t.TempDir(), "rootfs.img")
	stats, err := createRootfs(imagePath, buildTar(t, entries), injection{})
	if err != nil {
		t.Fatalf("createRootfs: %v", err)
	}

	// Unlike the go-diskfs path, the engine writes hardlinks and device nodes natively, so nothing is
	// dropped or copied around.
	want := buildStats{Files: 1, Dirs: 3, Symlinks: 1, Hardlinks: 1, Devices: 2}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestCreateRootfsInjectsFiles(t *testing.T) {
	entries := []tarEntry{
		{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "etc/hostname", Typeflag: tar.TypeReg, Mode: 0o644}, body: "deepcove\n"},
	}

	dir := t.TempDir()
	// Stand in for the compiled guest init binary and its launch script.
	initBin := filepath.Join(dir, "init")
	if err := os.WriteFile(initBin, []byte("\x7fELF-not-really"), 0o755); err != nil {
		t.Fatalf("write init: %v", err)
	}
	launch := filepath.Join(dir, "launch")
	if err := os.WriteFile(launch, []byte("exec claude\n"), 0o644); err != nil {
		t.Fatalf("write launch: %v", err)
	}

	imagePath := filepath.Join(dir, "rootfs.img")
	stats, err := createRootfs(imagePath, buildTar(t, entries), baseInjection(initBin))
	if err != nil {
		t.Fatalf("createRootfs: %v", err)
	}
	// One OCI file (hostname) plus the injected init binary. Nothing per session is baked in.
	if stats.Files != 2 {
		t.Errorf("Files = %d, want 2 (1 OCI + the init binary)", stats.Files)
	}
	// One OCI dir (/etc) plus the injected overlay mount point.
	if stats.Dirs != 2 {
		t.Errorf("Dirs = %d, want 2 (1 OCI + the overlay mount point)", stats.Dirs)
	}
	assertExt4(t, imagePath)
}

// Regression: the base image is mounted read only, so the guest init cannot mkdir its overlay mount
// point at runtime. It died with "mkdir /rom: read-only file system" and panicked the guest. Every
// bootable image must therefore carry that directory already.
//
// It must also carry NOTHING per session: the base is cached per image digest and shared by every
// session using that image, so a per session file baked here would leak between sessions.
func TestBaseInjectionBakesOverlayMountPointAndNothingPerSession(t *testing.T) {
	inj := baseInjection("/host/init")

	var dirs []string
	for _, d := range inj.Dirs {
		dirs = append(dirs, d.Target)
	}
	if !slices.Contains(dirs, guestinit.OverlayMountDir) {
		t.Errorf("injected dirs = %v, must contain the overlay mount point %q", dirs, guestinit.OverlayMountDir)
	}

	var files []string
	for _, f := range inj.Files {
		files = append(files, f.Target)
	}
	if !slices.Contains(files, guestinit.InitPath) {
		t.Errorf("injected files = %v, must contain the init binary %q", files, guestinit.InitPath)
	}
	if len(files) != 1 {
		t.Errorf("injected files = %v; only the init binary belongs in a shared base image", files)
	}
}

func TestWriteRootfsProducesExt4(t *testing.T) {
	entries := []tarEntry{
		{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "etc/hostname", Typeflag: tar.TypeReg, Mode: 0o644}, body: "deepcove\n"},
	}

	imagePath := filepath.Join(t.TempDir(), "rootfs.img")
	if _, err := createRootfs(imagePath, buildTar(t, entries), injection{}); err != nil {
		t.Fatalf("createRootfs: %v", err)
	}

	// ext4 superblock magic 0xEF53 sits at offset 0x438 (little endian), a cheap structural sanity check
	// that does not need a Linux mount. CI runs e2fsck + a real kernel mount for full validation.
	img, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	const magicOffset = 0x438
	if len(img) < magicOffset+2 {
		t.Fatalf("image too small: %d bytes", len(img))
	}
	if img[magicOffset] != 0x53 || img[magicOffset+1] != 0xEF {
		t.Errorf("ext4 magic = %#x %#x, want 0x53 0xEF", img[magicOffset], img[magicOffset+1])
	}
}

func TestWriteRootfsDeterministic(t *testing.T) {
	entries := []tarEntry{
		{hdr: tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: "alpha"},
		{hdr: tar.Header{Name: "b.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: "beta"},
	}

	dir := t.TempDir()
	first := filepath.Join(dir, "a.img")
	second := filepath.Join(dir, "b.img")
	if _, err := createRootfs(first, buildTar(t, entries), injection{}); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if _, err := createRootfs(second, buildTar(t, entries), injection{}); err != nil {
		t.Fatalf("second build: %v", err)
	}

	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("identical inputs produced different images; build is not deterministic")
	}
}

func TestPAXXattrs(t *testing.T) {
	records := map[string]string{
		"SCHILY.xattr.security.capability": "\x01\x00\x00\x02",
		"SCHILY.xattr.user.note":           "hi",
		"mtime":                            "1700000000", // not an xattr, must be ignored
	}
	got := paxXattrs(records)
	if len(got) != 2 {
		t.Fatalf("got %d xattrs, want 2: %v", len(got), got)
	}
	if string(got["security.capability"]) != "\x01\x00\x00\x02" {
		t.Errorf("security.capability = %q", got["security.capability"])
	}
	if _, ok := got["mtime"]; ok {
		t.Error("non-xattr PAX record leaked into xattrs")
	}
}

func TestPAXXattrsEmpty(t *testing.T) {
	if got := paxXattrs(nil); got != nil {
		t.Errorf("paxXattrs(nil) = %v, want nil", got)
	}
	if got := paxXattrs(map[string]string{"mtime": "123"}); got != nil {
		t.Errorf("paxXattrs with no xattr records = %v, want nil", got)
	}
}
