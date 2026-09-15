package vm

import (
	"os"
	"syscall"
	"testing"
)

// Guest device names come from the attach order, and both extra disks are optional. Hardcoding /dev/vdb
// for the home disk was correct only while it was the sole optional device.
func TestGuestDevices(t *testing.T) {
	cases := []struct {
		name     string
		home     string
		swap     string
		wantHome string
		wantSwap string
	}{
		{name: "neither disk"},
		{
			name: "home only, the shape before swap existed",
			home: "/disks/session.img", wantHome: "/dev/vdb",
		},
		{
			name: "both, in attach order",
			home: "/disks/session.img", swap: "/disks/swap.img",
			wantHome: "/dev/vdb", wantSwap: "/dev/vdc",
		},
		{
			// A session with no persistent home still gets swap, and it must not be told /dev/vdc.
			name: "swap only moves up to vdb",
			swap: "/disks/swap.img", wantSwap: "/dev/vdb",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, swap := guestDevices(tc.home, tc.swap)
			if home != tc.wantHome {
				t.Errorf("home: got %q, want %q", home, tc.wantHome)
			}
			if swap != tc.wantSwap {
				t.Errorf("swap: got %q, want %q", swap, tc.wantSwap)
			}
		})
	}
}

func TestSwapDiskBytes(t *testing.T) {
	cases := []struct {
		name   string
		memory uint64
		want   int64
	}{
		{name: "twice the memory", memory: 4 << 30, want: 8 << 30},
		{name: "a small vm", memory: 1 << 30, want: 2 << 30},
		// Nothing to size against means no swap disk, rather than a zero byte one.
		{name: "no memory configured", memory: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swapDiskBytes(tc.memory); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSwapDiskIsSparseAndReset(t *testing.T) {
	d := &Driver{disks: t.TempDir()}

	path, err := d.swapDisk("handle-1", 4<<30)
	if err != nil {
		t.Fatalf("swapDisk: %v", err)
	}
	if got := fileApparentSize(t, path); got != 8<<30 {
		t.Errorf("apparent size: got %d, want %d", got, int64(8<<30))
	}
	// Sparse, so the ceiling costs nothing until something is swapped out.
	if blocks := fileDiskBytes(t, path); blocks > 1<<20 {
		t.Errorf("occupies %d bytes on disk, want a sparse file", blocks)
	}

	// A second boot reuses the path but starts from a hole, so last boot's spill stops taking space.
	writeAt(t, path, 1<<20, make([]byte, 4<<20))
	if blocks := fileDiskBytes(t, path); blocks < 1<<20 {
		t.Fatalf("expected the write to occupy disk, got %d bytes", blocks)
	}
	if _, err := d.swapDisk("handle-1", 4<<30); err != nil {
		t.Fatalf("second swapDisk: %v", err)
	}
	if blocks := fileDiskBytes(t, path); blocks > 1<<20 {
		t.Errorf("still occupies %d bytes after reset, want it released", blocks)
	}
}

func TestSwapDiskSkippedWithoutMemory(t *testing.T) {
	d := &Driver{disks: t.TempDir()}
	path, err := d.swapDisk("handle-1", 0)
	if err != nil {
		t.Fatalf("swapDisk: %v", err)
	}
	if path != "" {
		t.Errorf("got %q, want no swap disk", path)
	}
}

// fileApparentSize is the size the filesystem reports, which for a sparse file is the ceiling rather
// than what it occupies.
func fileApparentSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return info.Size()
}

// fileDiskBytes is what the file actually occupies, from the allocated block count.
func fileDiskBytes(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no block count on this platform")
	}
	return st.Blocks * 512
}

func writeAt(t *testing.T, path string, off int64, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatalf("write: %v", err)
	}
}
