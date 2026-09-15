// Package vm runs coding agent sessions in per session virtual machines.
//
// It owns everything a session needs that does not depend on which hypervisor boots the guest: the image
// and rootfs pipeline, the disks, the guest services over vsock, readiness, and the console. A
// hypervisor is a Backend, and the two are Apple Virtualization.framework on macOS and Cloud Hypervisor
// on Linux.
package vm

import (
	"context"
	"io"
	"net"
	"time"
)

// Backend is a hypervisor. Boot turns a Spec into a running guest.
type Backend interface {
	Boot(spec Spec) (Machine, error)
	// Version names the hypervisor when it is usable on this host, and errors when it is not. It is
	// what the preflight check reports.
	Version() (string, error)
}

// Machine is one running guest, and is all this package needs from a hypervisor.
//
// Both directions of vsock are here because both are used: the host dials the guest for exec, control
// and config, and the guest dials the host for credentials and the LLM proxy.
type Machine interface {
	// Dial opens a connection to a service the guest is listening on.
	Dial(ctx context.Context, port uint32) (net.Conn, error)
	// Listen accepts connections the guest opens to the host.
	Listen(port uint32) (net.Listener, error)
	// Console is the guest's serial console, which has to be drained: it is a pipe, and a full one
	// blocks the guest mid boot.
	Console() io.ReadWriteCloser
	// Running reports whether the guest is still up, so a caller can skip a dial that cannot succeed.
	Running() bool
	// Stop tears the guest down and releases what the host holds for it. Safe to call twice.
	//
	// It must not return while the guest is still running. Exiting on top of a VM the hypervisor still
	// owns is how this process ends up unreapable, holding its database until the machine reboots.
	Stop() error
	// WaitStopped blocks until the guest has actually halted after being asked to shut itself down.
	WaitStopped(timeout time.Duration) error
}
