package vmassets

import "github.com/waltzofpearls/deepcove/internal/vmruntime"

// Artifact is one file a VM session needs, as it exists on a release and once it is unpacked.
type Artifact struct {
	// Name is what it is called in logs, on disk, and on the command line: "guest-kernel", "passt".
	Name string
	// Version is the upstream version, and the folder the unpacked File lives in.
	Version string
	// Asset is the file on the release, and File is the one inside it.
	Asset string
	File  string
}

// GuestKernel is the kernel every session boots, on any host.
func GuestKernel(goarch string) Artifact {
	return Artifact{
		Name:    "guest-kernel",
		Version: vmruntime.KernelVersion,
		Asset:   vmruntime.GuestKernelAsset(goarch),
		File:    "vmlinux",
	}
}

// CloudHypervisor is the VMM a Linux node boots sessions with.
func CloudHypervisor(goarch string) Artifact {
	return Artifact{
		Name:    "cloud-hypervisor",
		Version: vmruntime.CloudHypervisorVersion,
		Asset:   vmruntime.CloudHypervisorAsset(goarch),
		File:    "cloud-hypervisor",
	}
}

// Virtiofsd shares a host folder with the guest.
func Virtiofsd(goarch string) Artifact {
	return Artifact{
		Name:    "virtiofsd",
		Version: vmruntime.VirtiofsdVersion,
		Asset:   vmruntime.VirtiofsdAsset(goarch),
		File:    "virtiofsd",
	}
}

// Passt gives the guest a network without any privileges on the host.
func Passt(goarch string) Artifact {
	return Artifact{
		Name:    "passt",
		Version: vmruntime.PasstVersion,
		Asset:   vmruntime.PasstAsset(goarch),
		File:    "passt",
	}
}

// LinuxHost are the host binaries a Linux node needs to run VMs. A Mac needs none of them.
func LinuxHost(goarch string) []Artifact {
	return []Artifact{
		CloudHypervisor(goarch),
		Virtiofsd(goarch),
		Passt(goarch),
	}
}
