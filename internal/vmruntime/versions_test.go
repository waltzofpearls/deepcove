package vmruntime

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAssetNames(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "the guest kernel names no operating system", got: GuestKernelAsset("arm64"),
			want: "guest-kernel_" + KernelVersion + "_arm64.tar.gz"},
		{name: "a host binary names the platform", got: CloudHypervisorAsset("amd64"),
			want: "cloud-hypervisor_" + CloudHypervisorVersion + "_linux-amd64.tar.gz"},
		{name: "virtiofsd", got: VirtiofsdAsset("arm64"),
			want: "virtiofsd_" + VirtiofsdVersion + "_linux-arm64.tar.gz"},
		{name: "passt", got: PasstAsset("amd64"),
			want: "passt_" + PasstVersion + "_linux-amd64.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("asset = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestVersionsNameEveryArch(t *testing.T) {
	versions := Versions()
	for name, assets := range map[string]map[string]string{
		"guest kernel":     versions.GuestKernel.Assets,
		"cloud hypervisor": versions.CloudHypervisor.Assets,
		"virtiofsd":        versions.Virtiofsd.Assets,
		"passt":            versions.Passt.Assets,
	} {
		for _, goarch := range Arches {
			if assets[goarch] == "" {
				t.Errorf("%s has no asset for %s", name, goarch)
			}
		}
	}
}

func TestVersionsJSONIsCurrent(t *testing.T) {
	want, err := json.MarshalIndent(Versions(), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := os.ReadFile("versions.json")
	if err != nil {
		t.Fatalf("read versions.json: %v", err)
	}
	if string(got) != string(want)+"\n" {
		t.Fatal("versions.json is stale: run go generate ./internal/vmruntime")
	}
}
