// Command deepcove-guest-init is the PID 1 baked into a session VM's root filesystem. The kernel boots
// the read only ext4 base directly with init=/sbin/deepcove-init, and this binary overlays a writable
// layer, pivots into it, and launches the agent under tmux. It is cross compiled to the guest's Linux
// arch (pure Go, static, CGO off) and injected into the image at build time.
package main

import (
	"fmt"
	"os"

	"github.com/waltzofpearls/deepcove/internal/guestinit"
)

func main() {
	if err := guestinit.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "deepcove-guest-init:", err)
		os.Exit(1)
	}
}
