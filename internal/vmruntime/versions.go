// Package vmruntime declares the versions of everything a VM session needs that we did not write, and
// names the release assets that carry them.
//
// The release build reads versions.json, generated from here, so the two cannot drift. The version is
// part of every file name, which is also how the release decides whether it can copy an artifact from
// an earlier release instead of building it again.
package vmruntime

//go:generate go run ./gen

import "fmt"

const (
	// KernelVersion is the guest kernel, taken from a Kata Containers release.
	KernelVersion = "6.18.35-202"

	// KataRelease is the release that kernel came out of. Kept next to it so the release build can
	// take exactly this kernel with nothing to type by hand.
	KataRelease = "4.1.0"

	// CloudHypervisorVersion is the Linux VMM. v52.0 is the floor: below it, passt networking crashes
	// a guest with more than one CPU, which is every session we create.
	CloudHypervisorVersion = "v54.0"

	// VirtiofsdVersion shares a host folder with the guest. Cloud Hypervisor has no file sharing of
	// its own.
	VirtiofsdVersion = "v1.14.0"

	// PasstVersion gives the guest a network with no privileges on the host. Named by commit, because
	// their own versions carry underscores that would fight the separator in a file name.
	PasstVersion = "7e8d49b"
)

// Arches are the architectures every artifact is built for.
var Arches = []string{"amd64", "arm64"}

// Artifacts is every file a VM session needs that is not the deepcove binary. The guest kernel boots on
// any host; the other three are Linux host binaries, unused on a Mac.
type Artifacts struct {
	GuestKernel     GuestKernel `json:"guest_kernel"`
	CloudHypervisor Artifact    `json:"cloud_hypervisor"`
	Virtiofsd       Artifact    `json:"virtiofsd"`
	Passt           Artifact    `json:"passt"`
}

type Artifact struct {
	Version string `json:"version"`
	// Assets is the file name to look for on the release, per architecture. Every architecture is
	// listed, not just this one, because the release builds them all.
	Assets map[string]string `json:"assets"`
}

type GuestKernel struct {
	Artifact
	KataRelease string `json:"kata_release"`
}

// Versions is every artifact this build needs, and what versions.json holds.
func Versions() Artifacts {
	return Artifacts{
		GuestKernel: GuestKernel{
			Artifact:    Artifact{Version: KernelVersion, Assets: assetsByArch(GuestKernelAsset)},
			KataRelease: KataRelease,
		},
		CloudHypervisor: Artifact{
			Version: CloudHypervisorVersion,
			Assets:  assetsByArch(CloudHypervisorAsset),
		},
		Virtiofsd: Artifact{Version: VirtiofsdVersion, Assets: assetsByArch(VirtiofsdAsset)},
		Passt:     Artifact{Version: PasstVersion, Assets: assetsByArch(PasstAsset)},
	}
}

func assetsByArch(name func(goarch string) string) map[string]string {
	assets := make(map[string]string, len(Arches))
	for _, goarch := range Arches {
		assets[goarch] = name(goarch)
	}
	return assets
}

// GuestKernelAsset is the kernel for a guest architecture. No operating system in the name: a guest
// kernel is always Linux, and the same arm64 file serves an Apple Silicon Mac and an arm64 Linux host.
func GuestKernelAsset(goarch string) string {
	return fmt.Sprintf("guest-kernel_%s_%s.tar.gz", KernelVersion, goarch)
}

// CloudHypervisorAsset, VirtiofsdAsset and PasstAsset are host binaries, so they name the host platform.
func CloudHypervisorAsset(goarch string) string {
	return fmt.Sprintf("cloud-hypervisor_%s_linux-%s.tar.gz", CloudHypervisorVersion, goarch)
}

func VirtiofsdAsset(goarch string) string {
	return fmt.Sprintf("virtiofsd_%s_linux-%s.tar.gz", VirtiofsdVersion, goarch)
}

func PasstAsset(goarch string) string {
	return fmt.Sprintf("passt_%s_linux-%s.tar.gz", PasstVersion, goarch)
}
