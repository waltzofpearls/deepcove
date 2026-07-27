package applevf

import (
	"fmt"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/guestinit"
)

// vmSpec is the platform independent description of one session VM. The darwin boot code turns it into a
// Virtualization.framework configuration; keeping it separate lets the spec and kernel command line be
// built and tested on any OS, including the Linux CI runner where the vz cgo code is not compiled.
type vmSpec struct {
	// KernelPath is the uncompressed Linux kernel (the Kata style kernel with virtio built in) booted
	// directly, no firmware or bootloader.
	KernelPath string
	// InitrdPath is an optional initial ramdisk. Empty means boot straight onto the root device.
	InitrdPath string
	// Cmdline is the kernel command line. Build it with (kernelCmdline).String().
	Cmdline string

	// BaseImage is the read only ext4 rootfs (from the image pipeline), attached as the first virtio
	// block device (/dev/vda). ScratchImage is the writable per session disk (/dev/vdb) the guest
	// overlays on top. ScratchImage may be empty for a throwaway VM that overlays onto tmpfs instead.
	BaseImage    string
	ScratchImage string

	CPUCount    uint
	MemoryBytes uint64
}

func (s vmSpec) validate() error {
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

// kernelCmdline builds a Linux kernel command line for direct kernel boot. The guest console rides a
// virtio console (hvc0), and the root device is the read only ext4 base; the guest init sets up the
// overlay from the scratch disk after mount.
type kernelCmdline struct {
	// Console is the console device, normally "hvc0" for the virtio console serial port.
	Console string
	// Root is the root block device, normally "/dev/vda" (the read only base image).
	Root string
	// Init overrides the init binary (e.g. the deepcove guest init). Empty uses the kernel default.
	Init string
	// Extra are additional space free tokens appended verbatim (e.g. "quiet", "loglevel=4").
	Extra []string
}

func (c kernelCmdline) String() string {
	var tokens []string
	if c.Console != "" {
		tokens = append(tokens, "console="+c.Console)
	}
	if c.Root != "" {
		// The base is read only; the guest remounts via overlay, so the root itself stays ro.
		tokens = append(tokens, "root="+c.Root, "ro")
	}
	tokens = append(tokens, c.Extra...)
	if c.Init != "" {
		tokens = append(tokens, "init="+c.Init)
	}
	return strings.Join(tokens, " ")
}

// defaultCmdline is the standard boot line: virtio console, read only base as root, and PID 1 handed to
// the deepcove guest init baked into the image.
func defaultCmdline() kernelCmdline {
	return kernelCmdline{Console: "hvc0", Root: "/dev/vda", Init: guestinit.InitPath}
}
