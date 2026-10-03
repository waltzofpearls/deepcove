package cloudhv

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// readyWait is how long to wait for a process to create the socket that says it is up. One that dies
// instead is noticed sooner, through its exit.
const readyWait = 10 * time.Second

// process is one running cloud-hypervisor, and the host side files it was given.
type process struct {
	*vmproc.Process
	paths   paths
	console *os.File
}

// launch starts one VM and waits for its API socket to appear.
func launch(binary string, spec vm.Spec, p paths) (*process, error) {
	console, err := openConsoleFIFO(p.console)
	if err != nil {
		return nil, err
	}
	hypervisor, err := vmproc.Start("the vmm", binary, args(spec, p))
	if err != nil {
		_ = console.Close()
		return nil, err
	}

	proc := &process{Process: hypervisor, paths: p, console: console}
	if err := hypervisor.WaitForFile(p.api, readyWait); err != nil {
		_ = proc.Stop()
		return nil, err
	}
	return proc, nil
}

func (p *process) Console() io.ReadWriteCloser {
	return p.console
}

// Stop ends the VM and removes the sockets it was given.
func (p *process) Stop() error {
	err := errors.Join(p.Process.Stop(), p.console.Close())
	for _, path := range []string{p.paths.api, p.paths.vsock, p.paths.console} {
		err = errors.Join(err, vmproc.RemoveSocket(path))
	}
	return err
}

// Dial reaches a guest service over this VM's vsock.
func (p *process) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	return vmproc.DialGuest(ctx, p.paths.vsock, port)
}

// Listen accepts the connections the guest makes to the host.
func (p *process) Listen(port uint32) (net.Listener, error) {
	return vmproc.ListenGuest(p.paths.vsock, port)
}
