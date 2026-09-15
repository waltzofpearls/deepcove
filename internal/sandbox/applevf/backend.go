// Package applevf boots session VMs with Apple Virtualization.framework. It is the macOS backend for
// the vm package, which owns everything above the hypervisor.
package applevf

import (
	"log/slog"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// Backend is the Apple Virtualization.framework hypervisor. A VM is described by the spec it is booted
// from; the logger is here so a guest that stops listening says so without a stack dump.
type Backend struct {
	Logger *slog.Logger
}

// Boot starts one session VM.
func (b Backend) Boot(spec vm.Spec) (vm.Machine, error) {
	m, err := bootVM(spec, b.Logger)
	if err != nil {
		return nil, err // a nil interface, not a typed nil *machine
	}
	return m, nil
}

// Version reports whether this host can run VMs at all.
func (Backend) Version() (string, error) {
	return platformVersion()
}
