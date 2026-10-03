package applevf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// bootWait bounds the VM's process coming up: booting the VM and opening its sockets.
const bootWait = 30 * time.Second

// RunConfig is what `deepcove vm run` gets from deepcove: the VM, and the two sockets it talks over.
type RunConfig struct {
	Spec          vm.Spec
	VsockSocket   string
	ConsoleSocket string
}

// runningVM is one VM in its own `deepcove vm run` process, reached through sockets in its folder.
type runningVM struct {
	proc    *vmproc.Process
	dir     string
	vsock   string
	console net.Conn
}

func startVMProcess(self, ipcDir string, spec vm.Spec) (*runningVM, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	dir, err := vmproc.NewRunDir(ipcDir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*runningVM, error) { return nil, errors.Join(err, os.RemoveAll(dir)) }
	cfg := RunConfig{
		Spec:          spec,
		VsockSocket:   filepath.Join(dir, "vsock.sock"),
		ConsoleSocket: filepath.Join(dir, "console.sock"),
	}
	consoleListener, err := net.Listen("unix", cfg.ConsoleSocket)
	if err != nil {
		return fail(fmt.Errorf("applevf: listen for the console: %w", err))
	}
	defer func() { _ = consoleListener.Close() }()
	settings, err := json.Marshal(cfg)
	if err != nil {
		return fail(err)
	}
	file := filepath.Join(dir, "run.json")
	if err := os.WriteFile(file, settings, 0o600); err != nil {
		return fail(err)
	}
	proc, err := vmproc.Start("the vm", self, []string{"vm", "run", file})
	if err != nil {
		return fail(err)
	}
	// The vsock socket appears last, after the VM booted and dialed its console.
	if err := proc.WaitForFile(cfg.VsockSocket, bootWait); err != nil {
		return fail(errors.Join(err, proc.Stop()))
	}
	_ = consoleListener.(*net.UnixListener).SetDeadline(time.Now().Add(bootWait))
	console, err := consoleListener.Accept()
	if err != nil {
		return fail(errors.Join(fmt.Errorf("applevf: the VM never sent its console: %w", err), proc.Stop()))
	}
	return &runningVM{proc: proc, dir: dir, vsock: cfg.VsockSocket, console: console}, nil
}

func (m *runningVM) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	return vmproc.DialGuest(ctx, m.vsock, port)
}

func (m *runningVM) Listen(port uint32) (net.Listener, error) {
	return vmproc.ListenGuest(m.vsock, port)
}

func (m *runningVM) Console() io.ReadWriteCloser { return m.console }

func (m *runningVM) Running() bool { return m.proc.Running() }

// Stop signals the VM's process and kills it after a few seconds if it will not go: a jammed VM cannot
// hold deepcove up.
func (m *runningVM) Stop() error {
	return errors.Join(m.proc.Stop(), m.console.Close(), os.RemoveAll(m.dir))
}

func (m *runningVM) WaitStopped(timeout time.Duration) error { return m.proc.WaitStopped(timeout) }

// pidFile sits beside the vsock socket in the VM's folder, so a sweep finds both together.
func pidFile(vsockSocket string) string {
	return filepath.Join(filepath.Dir(vsockSocket), "vm.pid")
}
