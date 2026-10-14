package vm

import (
	"fmt"
	"os"
)

// Spec is the hypervisor independent description of one session VM. A Backend turns it into whatever
// its own configuration looks like, so the spec and the kernel command line can be built and tested on
// any OS, including a CI runner with no hypervisor at all.
type Spec struct {
	// KernelPath is the uncompressed Linux kernel (the Kata style kernel with virtio built in) booted
	// directly, no firmware or bootloader.
	KernelPath string
	// InitrdPath is an optional initial ramdisk. Empty means boot straight onto the root device.
	InitrdPath string
	// Cmdline is the kernel command line. Build it with (KernelCmdline).String().
	Cmdline string

	// BaseImage is the read only ext4 rootfs (from the image pipeline), attached as the first virtio
	// block device (/dev/vda). ScratchImage is the writable per session disk (/dev/vdb) the guest
	// overlays on top. ScratchImage may be empty for a throwaway VM that overlays onto tmpfs instead.
	BaseImage    string
	ScratchImage string
	// SwapImage is an optional raw disk the guest turns into swap. Attached after the scratch disk, so
	// the guest device names follow the attach order rather than a fixed layout.
	SwapImage string

	CPUCount    uint
	MemoryBytes uint64

	// Shares are host directories exposed over virtio-fs, one device each. The guest mounts them by tag.
	Shares []Share
	// HostPorts are the vsock ports the host serves the guest on. A backend that runs the VM in another
	// process listens on these for the guest from the start.
	HostPorts []uint32
	// NetworkAllow are private addresses the guest may still reach. The backend lets them through the
	// network it runs on the host, as the guest does in its own routes.
	NetworkAllow []string
}

// Share is one host directory shared into the guest.
type Share struct {
	Tag      string
	HostPath string
	ReadOnly bool
	// Folder is HostPath already opened and checked, where the backend can use it. Nil otherwise.
	Folder *os.File `json:"-"`
	// FolderID is the folder deepcove checked. A backend that opens shares by path opens it by this id,
	// so a path changed since the check cannot point the VM somewhere else.
	FolderID FolderID
}

func (s Spec) Validate() error {
	switch {
	case s.KernelPath == "":
		return fmt.Errorf("vm spec: kernel path is required")
	case s.BaseImage == "" && s.InitrdPath == "":
		// A session VM roots on the ext4 base; an initrd only boot (e.g. PUI PUI Linux for a boot smoke
		// test) has its rootfs in the ramdisk instead. One of the two must supply a root.
		return fmt.Errorf("vm spec: a base image or an initrd is required")
	case s.CPUCount == 0:
		return fmt.Errorf("vm spec: cpu count must be > 0")
	case s.MemoryBytes == 0:
		return fmt.Errorf("vm spec: memory must be > 0")
	}
	return nil
}
