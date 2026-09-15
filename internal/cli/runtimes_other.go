//go:build !darwin && !linux

package cli

// newVMRuntime has nothing to build here: Apple VF is macOS only and Cloud Hypervisor is Linux only.
// Such a node runs sessions in containers.
func newVMRuntime(runtimeDeps) runtimeEntry {
	return runtimeEntry{}
}
