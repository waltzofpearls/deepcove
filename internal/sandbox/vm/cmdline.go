package vm

import (
	"strings"

	"github.com/waltzofpearls/deepcove/internal/guestinit"
)

// KernelCmdline builds a Linux kernel command line for direct kernel boot. The guest console rides a
// virtio console (hvc0), and the root device is the read only ext4 base; the guest init sets up the
// overlay from the scratch disk after mount.
type KernelCmdline struct {
	// Console is the console device, normally "hvc0" for the virtio console serial port.
	Console string
	// Root is the root block device, normally "/dev/vda" (the read only base image).
	Root string
	// Init overrides the init binary (e.g. the deepcove guest init). Empty uses the kernel default.
	Init string
	// Extra are additional space free tokens appended verbatim (e.g. "quiet", "loglevel=4").
	Extra []string
}

func (c KernelCmdline) String() string {
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

// DefaultCmdline is the standard boot line: virtio console, read only base as root, and PID 1 handed to
// the deepcove guest init baked into the image.
func DefaultCmdline() KernelCmdline {
	return KernelCmdline{Console: "hvc0", Root: "/dev/vda", Init: guestinit.InitPath}
}
