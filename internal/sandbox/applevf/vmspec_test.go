package applevf

import (
	"strings"
	"testing"
)

func TestKernelCmdline(t *testing.T) {
	cases := []struct {
		name string
		in   kernelCmdline
		want string
	}{
		{"default", defaultCmdline(), "console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"},
		{"console only", kernelCmdline{Console: "ttyS0"}, "console=ttyS0"},
		{"with init", kernelCmdline{Console: "hvc0", Root: "/dev/vda", Init: "/sbin/deepcove-init"},
			"console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"},
		{"with extra", kernelCmdline{Console: "hvc0", Root: "/dev/vda", Extra: []string{"quiet", "loglevel=4"}},
			"console=hvc0 root=/dev/vda ro quiet loglevel=4"},
		{"empty", kernelCmdline{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The root device is always mounted read only; the guest overlays a scratch disk on top of it.
func TestKernelCmdlineRootIsReadOnly(t *testing.T) {
	got := kernelCmdline{Root: "/dev/vda"}.String()
	if !strings.Contains(got, "root=/dev/vda ro") {
		t.Errorf("root should be read only, got %q", got)
	}
}

func TestVMSpecValidate(t *testing.T) {
	valid := vmSpec{KernelPath: "/k/vmlinuz", BaseImage: "/i/base.ext4", CPUCount: 2, MemoryBytes: 512 << 20}
	cases := []struct {
		name    string
		mutate  func(*vmSpec)
		wantErr bool
	}{
		{"valid", func(*vmSpec) {}, false},
		{"no kernel", func(s *vmSpec) { s.KernelPath = "" }, true},
		{"no base image", func(s *vmSpec) { s.BaseImage = "" }, true},
		{"no cpu", func(s *vmSpec) { s.CPUCount = 0 }, true},
		{"no memory", func(s *vmSpec) { s.MemoryBytes = 0 }, true},
		{"scratch optional", func(s *vmSpec) { s.ScratchImage = "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.mutate(&spec)
			err := spec.validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// bootVM must reject an invalid spec before touching any platform machinery, on every OS. On macOS this
// stops short of a real boot (no kernel needed); on Linux bootVM is the not-supported stub. Either way an
// empty spec is an error.
func TestBootVMRejectsInvalidSpec(t *testing.T) {
	if _, err := bootVM(vmSpec{}); err == nil {
		t.Error("bootVM(empty spec) should error")
	}
}

// A well formed spec pointing at a nonexistent kernel must also error rather than boot: on macOS the boot
// loader fails to open the kernel, on Linux it is unsupported. This exercises bootVM past validation
// without needing a real kernel artifact or the virtualization entitlement.
func TestBootVMMissingKernelErrors(t *testing.T) {
	spec := vmSpec{
		KernelPath:  filepathJoinNonexistent(t),
		Cmdline:     defaultCmdline().String(),
		BaseImage:   filepathJoinNonexistent(t),
		CPUCount:    1,
		MemoryBytes: 256 << 20,
	}
	if _, err := bootVM(spec); err == nil {
		t.Error("bootVM with a missing kernel should error, not boot")
	}
}

func filepathJoinNonexistent(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/does-not-exist"
}
