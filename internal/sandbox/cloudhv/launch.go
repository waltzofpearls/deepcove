package cloudhv

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

const (
	// readyWait is how long to wait for a process to create the socket that says it is up. One that dies
	// instead is noticed sooner, through its exit.
	readyWait = 10 * time.Second
	readyPoll = 20 * time.Millisecond
)

// process is one running cloud-hypervisor, and the host side files it was given.
type process struct {
	*child
	paths   paths
	console *os.File
}

// launch starts one VM and waits for its API socket to appear.
func launch(binary string, spec vm.Spec, p paths) (*process, error) {
	console, err := openConsoleFIFO(p.console)
	if err != nil {
		return nil, err
	}
	c, err := startChild("the vmm", binary, args(spec, p))
	if err != nil {
		_ = console.Close()
		return nil, err
	}

	proc := &process{child: c, paths: p, console: console}
	if err := waitForFile(c, p.api, readyWait); err != nil {
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
	err := errors.Join(p.child.Stop(), p.console.Close())
	for _, path := range []string{p.paths.api, p.paths.vsock, p.paths.console} {
		err = errors.Join(err, removeSocket(path))
	}
	return err
}

// Dial reaches a guest service over this VM's vsock.
func (p *process) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	return dialGuest(ctx, p.paths.vsock, port)
}

// Listen accepts the connections the guest makes to the host.
func (p *process) Listen(port uint32) (net.Listener, error) {
	return listenGuest(p.paths.vsock, port)
}
