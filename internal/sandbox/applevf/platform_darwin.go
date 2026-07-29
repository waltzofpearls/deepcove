//go:build darwin

package applevf

import (
	"fmt"

	"github.com/Code-Hex/vz/v3"
)

// platformVersion reports whether this machine can run VMs, which is what preflight surfaces to the UI.
//
// vz exposes no direct availability query outside its own tests, so this probes by constructing the
// cheapest real object the runtime needs. On a macOS version or a binary that cannot support
// Virtualization.framework this fails, which is exactly the condition worth reporting. Note it does not
// prove the process carries the virtualization entitlement: that only shows up when a VM actually starts.
func platformVersion() (string, error) {
	if _, err := vz.NewGenericPlatformConfiguration(); err != nil {
		return "", fmt.Errorf("applevf: Virtualization.framework is unavailable: %w", err)
	}
	return "Apple Virtualization.framework", nil
}
