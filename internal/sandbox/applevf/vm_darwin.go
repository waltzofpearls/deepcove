//go:build darwin

package applevf

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// machine is a running session VM plus the host side ends of its virtio console and the resources that
// must outlive the VM (the console pipes). Stop tears the VM down and releases them.
type machine struct {
	vm      *vz.VirtualMachine
	console io.ReadWriteCloser
	closers []io.Closer
	dialer  *dialer
}

// bootVM builds a Virtualization.framework VM from spec and starts it. The guest console is exposed on
// the returned machine so callers can read boot output and (later) drive the agent. Running a VM needs
// the com.apple.security.virtualization entitlement and a codesigned binary; building does not.
func bootVM(spec vm.Spec, logger *slog.Logger) (*machine, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	bootOpts := []vz.LinuxBootLoaderOption{vz.WithCommandLine(spec.Cmdline)}
	if spec.InitrdPath != "" {
		bootOpts = append(bootOpts, vz.WithInitrd(spec.InitrdPath))
	}
	bootLoader, err := vz.NewLinuxBootLoader(spec.KernelPath, bootOpts...)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, spec.CPUCount, spec.MemoryBytes)
	if err != nil {
		return nil, fmt.Errorf("vm configuration: %w", err)
	}

	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, fmt.Errorf("platform configuration: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platform)

	storage, err := storageDevices(spec)
	if err != nil {
		return nil, err
	}
	config.SetStorageDevicesVirtualMachineConfiguration(storage)

	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("nat attachment: %w", err)
	}
	network, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("network device: %w", err)
	}
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{network})

	socket, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("vsock device: %w", err)
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socket})

	shares, err := sharedDirectories(spec)
	if err != nil {
		return nil, err
	}
	if len(shares) > 0 {
		config.SetDirectorySharingDevicesVirtualMachineConfiguration(shares)
	}

	console, consoleCloser, err := serialConsole(config)
	if err != nil {
		return nil, err
	}

	if _, err := config.Validate(); err != nil {
		_ = console.Close()
		return nil, fmt.Errorf("invalid vm configuration: %w", err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		_ = console.Close()
		return nil, fmt.Errorf("create vm: %w", err)
	}
	if err := vm.Start(); err != nil {
		_ = console.Close()
		return nil, fmt.Errorf("start vm: %w", err)
	}

	m := &machine{vm: vm, console: console, closers: consoleCloser}
	m.dialer = newDialer(m.connectToPort, logger)
	return m, nil
}

// storageDevices attaches the read only base as /dev/vda and, when present, the writable scratch disk as
// /dev/vdb. Device order is the attach order, which the kernel command line's root=/dev/vda relies on.
// An initrd only boot (no base image) has no block devices at all.
func storageDevices(spec vm.Spec) ([]vz.StorageDeviceConfiguration, error) {
	if spec.BaseImage == "" {
		return nil, nil
	}
	base, err := blockDevice(spec.BaseImage, true)
	if err != nil {
		return nil, fmt.Errorf("base image: %w", err)
	}
	devices := []vz.StorageDeviceConfiguration{base}
	if spec.ScratchImage != "" {
		scratch, err := blockDevice(spec.ScratchImage, false)
		if err != nil {
			return nil, fmt.Errorf("scratch image: %w", err)
		}
		devices = append(devices, scratch)
	}
	if spec.SwapImage != "" {
		swap, err := blockDevice(spec.SwapImage, false)
		if err != nil {
			return nil, fmt.Errorf("swap image: %w", err)
		}
		devices = append(devices, swap)
	}
	return devices, nil
}

func blockDevice(path string, readOnly bool) (*vz.VirtioBlockDeviceConfiguration, error) {
	attachment, err := vz.NewDiskImageStorageDeviceAttachment(path, readOnly)
	if err != nil {
		return nil, err
	}
	return vz.NewVirtioBlockDeviceConfiguration(attachment)
}

// serialConsole wires a virtio console serial port to a host pipe pair, returning a duplex stream: reads
// drain the guest's console output, writes feed its console input. The returned closers own the four
// underlying pipe files and must be released on teardown.
func serialConsole(config *vz.VirtualMachineConfiguration) (io.ReadWriteCloser, []io.Closer, error) {
	// guest stdout: guest writes to guestOutW, host reads from guestOutR.
	guestOutR, guestOutW, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("console output pipe: %w", err)
	}
	// guest stdin: host writes to guestInW, guest reads from guestInR.
	guestInR, guestInW, err := os.Pipe()
	if err != nil {
		_ = guestOutR.Close()
		_ = guestOutW.Close()
		return nil, nil, fmt.Errorf("console input pipe: %w", err)
	}

	attachment, err := vz.NewFileHandleSerialPortAttachment(guestInR, guestOutW)
	if err != nil {
		return nil, nil, fmt.Errorf("serial attachment: %w", err)
	}
	port, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return nil, nil, fmt.Errorf("serial port: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{port})

	stream := &consoleStream{r: guestOutR, w: guestInW}
	closers := []io.Closer{guestOutR, guestOutW, guestInR, guestInW}
	return stream, closers, nil
}

// consoleStream presents the host ends of the console pipes as one duplex stream.
type consoleStream struct {
	r *os.File
	w *os.File
}

func (c *consoleStream) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *consoleStream) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *consoleStream) Close() error {
	err := c.r.Close()
	if werr := c.w.Close(); werr != nil && err == nil {
		err = werr
	}
	return err
}

// Console returns the guest console stream.
func (m *machine) Console() io.ReadWriteCloser {
	return m.console
}

// Running reports whether the guest is still up.
func (m *machine) Running() bool {
	return m.vm.State() == vz.VirtualMachineStateRunning
}

// stopWait bounds the wait for the guest to actually halt. RequestStop only asks, and a VM here runs as
// threads of this process, so returning while it is still up lets the caller exit on top of live VM
// threads. That is what makes a deepcove no signal can reap.
const stopWait = 2 * time.Second

// Stop halts the guest and releases the host side resources.
//
// The polite shutdown over the control channel has already been asked for by the caller. This is the
// fallback, and Virtualization.framework's own stop request is all it can do: the Kata guest kernel has
// no gpio-keys driver, so a guest that ignored the control channel will ignore this too.
//
// The console pipes are closed last, after the guest has gone: closing them under a guest that is still
// writing turns its console into an error.
func (m *machine) Stop() error {
	var err error
	if m.vm.CanRequestStop() {
		_, err = m.vm.RequestStop()
	}
	// Waited for even when the request failed, since the VM may already be on its way down.
	if waitErr := m.WaitStopped(stopWait); waitErr != nil && err == nil {
		err = waitErr
	}
	for _, c := range m.closers {
		_ = c.Close()
	}
	return err
}

// WaitStopped waits for the VM to reach a stopped state after a shutdown request.
func (m *machine) WaitStopped(timeout time.Duration) error {
	deadline := time.After(timeout)
	states := m.vm.StateChangedNotify()
	for {
		if state := m.vm.State(); state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
			return nil
		}
		select {
		case <-states:
		case <-deadline:
			return fmt.Errorf("applevf: the guest did not halt within %s", timeout)
		}
	}
}

// Dial connects to a guest vsock port.
func (m *machine) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	return m.dialer.Dial(ctx, port)
}

// PendingDials is how many dials to this guest have not returned.
func (m *machine) PendingDials() int {
	return m.dialer.Pending()
}

// connectToPort is the one Virtualization.framework call the dial path makes.
func (m *machine) connectToPort(port uint32) (net.Conn, error) {
	device, err := m.socketDevice()
	if err != nil {
		return nil, err
	}
	conn, err := device.Connect(port)
	if err != nil {
		return nil, err // a nil interface, not a typed nil connection
	}
	return conn, nil
}

// Listen accepts connections the guest opens to the host.
func (m *machine) Listen(port uint32) (net.Listener, error) {
	device, err := m.socketDevice()
	if err != nil {
		return nil, err
	}
	return device.Listen(port)
}

// socketDevice returns the VM's single virtio socket device, which the boot configuration always
// attaches.
func (m *machine) socketDevice() (*vz.VirtioSocketDevice, error) {
	devices := m.vm.SocketDevices()
	if len(devices) == 0 {
		return nil, fmt.Errorf("applevf: the VM has no vsock device: %w", sandbox.ErrNotSupported)
	}
	return devices[0], nil
}

// sharedDirectories builds one virtio-fs device per share. Each gets its own device rather than a single
// multi-directory share, so a mount that fails to attach cannot take the others with it.
func sharedDirectories(spec vm.Spec) ([]vz.DirectorySharingDeviceConfiguration, error) {
	out := make([]vz.DirectorySharingDeviceConfiguration, 0, len(spec.Shares))
	for _, s := range spec.Shares {
		dir, err := vz.NewSharedDirectory(s.HostPath, s.ReadOnly)
		if err != nil {
			return nil, fmt.Errorf("share %s: %w", s.HostPath, err)
		}
		single, err := vz.NewSingleDirectoryShare(dir)
		if err != nil {
			return nil, fmt.Errorf("share %s: %w", s.HostPath, err)
		}
		device, err := vz.NewVirtioFileSystemDeviceConfiguration(s.Tag)
		if err != nil {
			return nil, fmt.Errorf("virtio-fs device %s: %w", s.Tag, err)
		}
		device.SetDirectoryShare(single)
		out = append(out, device)
	}
	return out, nil
}
