package vm

import (
	"strings"
	"testing"
)

func TestKernelCmdlineString(t *testing.T) {
	cases := []struct {
		name string
		in   KernelCmdline
		want string
	}{
		{"default", DefaultCmdline(), "console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"},
		{"console only", KernelCmdline{Console: "ttyS0"}, "console=ttyS0"},
		{"with init", KernelCmdline{Console: "hvc0", Root: "/dev/vda", Init: "/sbin/deepcove-init"},
			"console=hvc0 root=/dev/vda ro init=/sbin/deepcove-init"},
		{"with extra", KernelCmdline{Console: "hvc0", Root: "/dev/vda", Extra: []string{"quiet", "loglevel=4"}},
			"console=hvc0 root=/dev/vda ro quiet loglevel=4"},
		{"empty", KernelCmdline{}, ""},
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
	got := KernelCmdline{Root: "/dev/vda"}.String()
	if !strings.Contains(got, "root=/dev/vda ro") {
		t.Errorf("root should be read only, got %q", got)
	}
}

func TestSpecValidate(t *testing.T) {
	valid := Spec{KernelPath: "/k/vmlinuz", BaseImage: "/i/base.ext4", CPUCount: 2, MemoryBytes: 512 << 20}
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"valid", func(*Spec) {}, false},
		{"no kernel", func(s *Spec) { s.KernelPath = "" }, true},
		{"no base image", func(s *Spec) { s.BaseImage = "" }, true},
		{"no cpu", func(s *Spec) { s.CPUCount = 0 }, true},
		{"no memory", func(s *Spec) { s.MemoryBytes = 0 }, true},
		{"scratch optional", func(s *Spec) { s.ScratchImage = "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.mutate(&spec)
			err := spec.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
