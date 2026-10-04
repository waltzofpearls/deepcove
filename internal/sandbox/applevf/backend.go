// Package applevf boots session VMs with Apple Virtualization.framework. It is the macOS backend for
// the vm package, which owns everything above the hypervisor.
package applevf

import (
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// Backend is the Apple Virtualization.framework hypervisor. A VM is described by the spec it is booted
// from.
type Backend struct {
	// Self is this deepcove binary. Each VM runs in its own `deepcove vm run`, so a VM that jams takes
	// only that process with it.
	Self string
	// IPCDir holds one private folder per running VM, with the sockets deepcove reaches it through.
	IPCDir string
}

// Boot starts one session VM in its own process.
func (b Backend) Boot(spec vm.Spec) (vm.Machine, error) {
	vmProcess, err := startVMProcess(b.Self, b.IPCDir, spec)
	if err != nil {
		return nil, err // a nil interface, not a typed nil *runningVM
	}
	return vmProcess, nil
}

// Version reports whether this host can run VMs at all.
func (Backend) Version() (string, error) {
	return platformVersion()
}
