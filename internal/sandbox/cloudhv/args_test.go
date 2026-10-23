package cloudhv

import (
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

func testSpec() vm.Spec {
	return vm.Spec{
		KernelPath:  "/k/vmlinux",
		Cmdline:     vm.DefaultCmdline().String(),
		BaseImage:   "/i/base.ext4",
		CPUCount:    2,
		MemoryBytes: 4 << 30,
	}
}

func testPaths() paths {
	return paths{api: "/r/api.sock", vsock: "/r/vsock.sock", console: "/r/console"}
}

// argFor returns the value that follows flag.
func argFor(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("%s is missing from %v", flag, argv)
	return ""
}

func TestArgs(t *testing.T) {
	argv := args(testSpec(), testPaths())
	cases := []struct {
		flag string
		want string
	}{
		{"--api-socket", "path=/r/api.sock"},
		{"--kernel", "/k/vmlinux"},
		{"--cmdline", "console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"},
		{"--cpus", "boot=2,nested=off"},
		{"--memory", "size=4294967296,shared=on"},
		{"--console", "file=/r/console"},
		{"--serial", "off"},
		{"--vsock", "cid=3,socket=/r/vsock.sock"},
		{"--disk", "path=/i/base.ext4,readonly=on,image_type=raw"},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			if got := argFor(t, argv, tc.flag); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.flag, got, tc.want)
			}
		})
	}
}

// Attach order is the device order, and the kernel command line roots on /dev/vda.
func TestArgsDiskOrder(t *testing.T) {
	spec := testSpec()
	spec.ScratchImage = "/d/home.img"
	spec.SwapImage = "/d/swap.img"

	got := diskArgs(spec)
	want := []string{
		"path=/i/base.ext4,readonly=on,image_type=raw",
		"path=/d/home.img,image_type=raw",
		"path=/d/swap.img,image_type=raw",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("disk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestArgsShares(t *testing.T) {
	spec := testSpec()
	spec.Shares = []vm.Share{{Tag: "dcshare0", HostPath: "/w"}, {Tag: "dcshare1", HostPath: "/x"}}
	p := testPaths()
	p.fsSockets = []string{"/r/fs0.sock", "/r/fs1.sock"}

	argv := strings.Join(args(spec, p), " ")
	for _, want := range []string{"tag=dcshare0,socket=/r/fs0.sock", "tag=dcshare1,socket=/r/fs1.sock"} {
		if !strings.Contains(argv, want) {
			t.Errorf("%q is missing from %s", want, argv)
		}
	}
}

// A VM with no network socket must not be given a network device at all.
func TestArgsNetwork(t *testing.T) {
	argv := strings.Join(args(testSpec(), testPaths()), " ")
	if strings.Contains(argv, "--net") {
		t.Errorf("a spec with no network socket should have no --net: %s", argv)
	}

	p := testPaths()
	p.netSocket = "/r/net.sock"
	argv = strings.Join(args(testSpec(), p), " ")
	if !strings.Contains(argv, "vhost_user=on,socket=/r/net.sock") {
		t.Errorf("the passt socket is missing: %s", argv)
	}
}

// An initrd boot has no root disk, and the kernel needs the ramdisk instead.
func TestArgsInitrd(t *testing.T) {
	spec := testSpec()
	spec.BaseImage = ""
	spec.InitrdPath = "/k/initramfs"

	argv := args(spec, testPaths())
	if got := argFor(t, argv, "--initramfs"); got != "/k/initramfs" {
		t.Errorf("--initramfs = %q", got)
	}
	if strings.Contains(strings.Join(argv, " "), "--disk") {
		t.Error("an initrd boot should attach no disks")
	}
}

func TestCommandLineQuotesTheCmdline(t *testing.T) {
	got := vmproc.CommandLine("/usr/bin/cloud-hypervisor", args(testSpec(), testPaths()))
	if !strings.Contains(got, `"console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"`) {
		t.Errorf("the command line should be runnable as printed: %s", got)
	}
}
