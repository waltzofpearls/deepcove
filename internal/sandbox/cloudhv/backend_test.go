package cloudhv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// recordingStub writes the arguments it was called with to <name>.args, creates every socket path it was
// told about, and then waits to be signalled.
func recordingStub(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := `#!/bin/sh
echo "$@" > "` + path + `.args"
for a in "$@"; do
  case "$a" in
    path=*) : > "${a#path=}" ;;
    */*.sock) : > "$a" ;;
  esac
done
sleep 30
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var (
	_ vm.Backend = Backend{}
	_ vm.Machine = (*machine)(nil)
)

func testBackend(t *testing.T) (Backend, string) {
	t.Helper()
	dir := shortTempDir(t)
	return Backend{
		CloudHypervisor: recordingStub(t, dir, "chv"),
		Virtiofsd:       recordingStub(t, dir, "vfsd"),
		Passt:           recordingStub(t, dir, "passt"),
		Self:            "/usr/local/bin/deepcove",
		IPCDir:          filepath.Join(dir, "ipc"),
	}, dir
}

func TestBootStartsTheHelpersFirst(t *testing.T) {
	backend, dir := testBackend(t)
	spec := testSpec()
	spec.Shares = []vm.Share{{Tag: "dcshare0", HostPath: "/work"}}

	m, err := backend.Boot(spec)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Stop() }()

	fsArgs := readArgs(t, dir, "vfsd")
	for _, want := range []string{"--shared-dir /work", "--tag dcshare0", "--sandbox none"} {
		if !strings.Contains(fsArgs, want) {
			t.Errorf("virtiofsd was not told %q: %s", want, fsArgs)
		}
	}
	if netArgs := readArgs(t, dir, "passt"); !strings.Contains(netArgs, "--vhost-user") {
		t.Errorf("passt was not put in vhost-user mode: %s", netArgs)
	}
	vmmArgs := readArgs(t, dir, "chv")
	for _, want := range []string{"--fs tag=dcshare0", "--net vhost_user=on", "--vsock cid=3"} {
		if !strings.Contains(vmmArgs, want) {
			t.Errorf("the vmm was not told %q: %s", want, vmmArgs)
		}
	}
}

// A read only share must stay read only in the guest.
func TestBootPassesReadOnlyShares(t *testing.T) {
	backend, dir := testBackend(t)
	spec := testSpec()
	spec.Shares = []vm.Share{{Tag: "dcshare0", HostPath: "/work", ReadOnly: true}}

	m, err := backend.Boot(spec)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Stop() }()

	if args := readArgs(t, dir, "vfsd"); !strings.Contains(args, "--readonly") {
		t.Errorf("a read only share was shared writable: %s", args)
	}
}

// Every VM gets its own directory, and stopping it takes the directory with it.
func TestStopRemovesTheIPCDirectory(t *testing.T) {
	backend, _ := testBackend(t)

	m, err := backend.Boot(testSpec())
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	ipcDir := m.(*machine).ipcDir
	if _, err := os.Stat(ipcDir); err != nil {
		t.Fatalf("the ipc directory is missing: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := os.Stat(ipcDir); !os.IsNotExist(err) {
		t.Errorf("the ipc directory survived: %v", err)
	}
	for _, helper := range m.(*machine).helpers {
		if helper.Running() {
			t.Errorf("%s is still running", helper.Name())
		}
	}
}

// A helper that will not start must not leave the VM half up.
func TestBootCleansUpAfterAFailedHelper(t *testing.T) {
	backend, dir := testBackend(t)
	backend.Virtiofsd = filepath.Join(dir, "missing-virtiofsd")
	spec := testSpec()
	spec.Shares = []vm.Share{{Tag: "dcshare0", HostPath: "/work"}}

	if _, err := backend.Boot(spec); err == nil {
		t.Fatal("Boot should fail when virtiofsd cannot start")
	}
	entries, err := os.ReadDir(backend.IPCDir)
	if err != nil {
		t.Fatalf("read the ipc directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed boot left %d directories behind", len(entries))
	}
}

func TestBackendValidate(t *testing.T) {
	cases := []struct {
		name    string
		backend Backend
	}{
		{"no vmm", Backend{Virtiofsd: "/v", IPCDir: "/r"}},
		{"no virtiofsd", Backend{CloudHypervisor: "/c", IPCDir: "/r"}},
		{"no ipc dir", Backend{CloudHypervisor: "/c", Virtiofsd: "/v"}},
		{"passt with no deepcove to guard it", Backend{CloudHypervisor: "/c", Virtiofsd: "/v", Passt: "/p", IPCDir: "/r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.backend.Boot(testSpec()); err == nil {
				t.Error("an incomplete backend should not boot")
			}
		})
	}
}

func readArgs(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name+".args"))
	if err != nil {
		t.Fatalf("read %s args: %v", name, err)
	}
	return string(body)
}

// shortTempDir keeps socket paths inside the platform limit; t.TempDir() is already too long on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
