package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

func TestVersionReportJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := newVersionReport().writeJSON(&buf); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	var got versionReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "kernel version", got: got.VM.GuestKernel.Version, want: vmruntime.KernelVersion},
		{name: "kata release", got: got.VM.GuestKernel.KataRelease, want: vmruntime.KataRelease},
		{name: "kernel asset", got: got.VM.GuestKernel.Assets["arm64"],
			want: vmruntime.GuestKernelAsset("arm64")},
		{name: "cloud hypervisor", got: got.VM.CloudHypervisor.Version,
			want: vmruntime.CloudHypervisorVersion},
		{name: "virtiofsd", got: got.VM.Virtiofsd.Version, want: vmruntime.VirtiofsdVersion},
		{name: "passt", got: got.VM.Passt.Version, want: vmruntime.PasstVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestVersionReportText(t *testing.T) {
	text := newVersionReport().text()
	want := []string{
		"version:", "go:", "os/arch:",
		vmruntime.KernelVersion, vmruntime.CloudHypervisorVersion,
		vmruntime.VirtiofsdVersion, vmruntime.PasstVersion,
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("output does not mention %q:\n%s", w, text)
		}
	}
}
