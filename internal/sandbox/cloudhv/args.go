package cloudhv

import (
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// guestCID is the guest's vsock address. Every VM gets its own socket, so they can all share one.
const guestCID = 3

// rawImage names the format of every disk we attach. Left to guess, the VMM makes the first sector read
// only, and that is where the guest writes its filesystem and swap headers.
const rawImage = "image_type=raw"

// paths are the host side files one VM's process needs.
type paths struct {
	api     string
	vsock   string
	console string
	// fsSockets are one virtiofsd socket per share, in the same order as the spec's shares.
	fsSockets []string
	// netSocket is passt's vhost-user socket. Empty leaves the guest with no network.
	netSocket string
}

// args builds the cloud-hypervisor command line for one VM.
func args(spec vm.Spec, p paths) []string {
	out := []string{
		"--api-socket", "path=" + p.api,
		"--kernel", spec.KernelPath,
		"--cmdline", spec.Cmdline,
		"--cpus", fmt.Sprintf("boot=%d", spec.CPUCount),
		// shared=on, or the VMM refuses every vhost-user device: the file shares and the network.
		"--memory", fmt.Sprintf("size=%d,shared=on", spec.MemoryBytes),
		"--console", "file=" + p.console,
		"--serial", "off",
		"--vsock", fmt.Sprintf("cid=%d,socket=%s", guestCID, p.vsock),
	}
	if spec.InitrdPath != "" {
		out = append(out, "--initramfs", spec.InitrdPath)
	}
	if disks := diskArgs(spec); len(disks) > 0 {
		out = append(out, "--disk")
		out = append(out, disks...)
	}
	if len(p.fsSockets) > 0 {
		out = append(out, "--fs")
		for i, socket := range p.fsSockets {
			out = append(out, fmt.Sprintf("tag=%s,socket=%s", spec.Shares[i].Tag, socket))
		}
	}
	if p.netSocket != "" {
		// passt creates the socket, so the VMM connects to it, which is the client default.
		out = append(out, "--net", "vhost_user=on,socket="+p.netSocket)
	}
	return out
}

// diskArgs attaches the base read only, then the scratch and swap disks. Device order is the attach
// order, which the kernel command line's root=/dev/vda relies on.
func diskArgs(spec vm.Spec) []string {
	var out []string
	if spec.BaseImage != "" {
		out = append(out, "path="+spec.BaseImage+",readonly=on,"+rawImage)
	}
	for _, disk := range []string{spec.ScratchImage, spec.SwapImage} {
		if disk != "" {
			out = append(out, "path="+disk+","+rawImage)
		}
	}
	return out
}
