//go:build linux

package guestinit

import (
	"fmt"
	"os/exec"
)

// enableSwap formats device as swap and switches it on.
//
// The writable layer is a tmpfs, so guest disk and memory come out of one budget. Without swap, a build
// that writes a couple of GB does not fail: the kernel kills the agent. tmpfs pages are swappable, so
// this turns that into slowness instead of death.
//
// Best effort: a session with no swap is the old behaviour, not a broken one.
func enableSwap(device string) error {
	// mkswap every boot rather than only the first. The header is a few kilobytes, nothing on the device
	// outlives a boot, and detecting an existing signature would need a tool the image does not ship.
	if out, err := exec.Command("mkswap", device).CombinedOutput(); err != nil { //nolint:gosec // device comes from the host config
		return fmt.Errorf("mkswap %s: %w: %s", device, err, out)
	}
	if out, err := exec.Command("swapon", device).CombinedOutput(); err != nil { //nolint:gosec // same
		return fmt.Errorf("swapon %s: %w: %s", device, err, out)
	}
	return nil
}
