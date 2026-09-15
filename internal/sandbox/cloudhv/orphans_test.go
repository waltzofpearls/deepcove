package cloudhv

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestOrphanPIDs(t *testing.T) {
	ipcDir := "/run/user/1000/deepcove"
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{name: "the vmm", cmdline: "cloud-hypervisor\x00--api-socket\x00path=" + ipcDir + "/ab12/api.sock", want: true},
		{name: "a virtiofsd", cmdline: "virtiofsd\x00--socket-path\x00" + ipcDir + "/ab12/fs0.sock", want: true},
		{name: "passt", cmdline: "passt\x00--socket\x00" + ipcDir + "/ab12/net.sock", want: true},
		{name: "someone else's vmm", cmdline: "cloud-hypervisor\x00--api-socket\x00path=/run/user/1001/deepcove/cd34/api.sock"},
		{name: "an unrelated process", cmdline: "sleep\x00600"},
		// The directory itself is not a socket in it, and deepcove holds it open for every VM.
		{name: "deepcove itself", cmdline: "deepcove\x00start"},
	}

	proc := t.TempDir()
	var want []int
	for i, tc := range cases {
		pid := 100 + i
		dir := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(tc.cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
		if tc.want {
			want = append(want, pid)
		}
	}
	// Things that are not processes at all, which /proc is full of.
	if err := os.Mkdir(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := orphanPIDs(proc, ipcDir)
	slices.Sort(got)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pid %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestSweepOrphansRemovesWhatTheyUsed(t *testing.T) {
	ipcDir := t.TempDir()
	vmDir := filepath.Join(ipcDir, "ab12")
	if err := os.Mkdir(vmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "api.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// No /proc here, so nothing is killed: this is the "the processes are already gone" case.
	procDir = t.TempDir()
	t.Cleanup(func() { procDir = "/proc" })

	stopped, err := Backend{IPCDir: ipcDir}.SweepOrphans()
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if stopped != 0 {
		t.Errorf("stopped %d processes, want none", stopped)
	}
	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Errorf("%s is still there: %v", vmDir, err)
	}
}

func TestSweepOrphansWithNothingToDo(t *testing.T) {
	stopped, err := Backend{IPCDir: filepath.Join(t.TempDir(), "never-created")}.SweepOrphans()
	if err != nil || stopped != 0 {
		t.Errorf("got %d, %v, want 0 and no error", stopped, err)
	}
}
