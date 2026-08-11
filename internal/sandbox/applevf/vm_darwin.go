//go:build darwin

package applevf

import (
	"fmt"
	"io"
	"os"

	"github.com/Code-Hex/vz/v3"
)

// machine is a running session VM plus the host side ends of its virtio console and the resources that
// must outlive the VM (the console pipes). Stop tears the VM down and releases them.
type machine struct {
	vm           *vz.VirtualMachine
	credListener *vz.VirtioSocketListener
	cfgListener  *vz.VirtioSocketListener
	consoleLog   *consoleBuffer
	console      io.ReadWriteCloser
	closers      []io.Closer
}

// bootVM builds a Virtualization.framework VM from spec and starts it. The guest console is exposed on
// the returned machine so callers can read boot output and (later) drive the agent. Running a VM needs
// the com.apple.security.virtualization entitlement and a codesigned binary; building does not.
func bootVM(spec vmSpec) (*machine, error) {
	if err := spec.validate(); err != nil {
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

	return &machine{vm: vm, console: console, closers: consoleCloser}, nil
}

// storageDevices attaches the read only base as /dev/vda and, when present, the writable scratch disk as
// /dev/vdb. Device order is the attach order, which the kernel command line's root=/dev/vda relies on.
// An initrd only boot (no base image) has no block devices at all.
func storageDevices(spec vmSpec) ([]vz.StorageDeviceConfiguration, error) {
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
func (m *machine) Console() io.ReadWriteCloser { return m.console }

// Stop halts the guest and releases the host side resources.
//
// It asks the guest to power itself off over the control channel first, which also gives it a chance to
// hand back refreshed credentials. Virtualization.framework's own RequestStop is only a fallback: the
// Kata guest kernel has no gpio-keys driver, so the guest never sees that request.
func (m *machine) Stop() error {
	err := m.Shutdown()
	if err != nil && m.vm.CanRequestStop() {
		if _, rerr := m.vm.RequestStop(); rerr == nil {
			err = nil
		}
	}
	if m.credListener != nil {
		_ = m.credListener.Close()
	}
	if m.cfgListener != nil {
		_ = m.cfgListener.Close()
	}
	for _, c := range m.closers {
		_ = c.Close()
	}
	return err
}

// sharedDirectories builds one virtio-fs device per share. Each gets its own device rather than a single
// multi-directory share, so a mount that fails to attach cannot take the others with it.
func sharedDirectories(spec vmSpec) ([]vz.DirectorySharingDeviceConfiguration, error) {
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
