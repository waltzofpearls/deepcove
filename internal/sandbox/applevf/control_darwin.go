//go:build darwin

package applevf

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestctl"
)

// shutdownTimeout bounds how long we wait for the guest to halt on request before falling back to
// stopping the VM outright.
const shutdownTimeout = 20 * time.Second

// serveCredentials runs the host side credential broker for this VM: the guest dials in at boot to fetch
// its stored login and again at shutdown to hand back whatever the agent refreshed.
//
// Unlike exec and control, the host listens here, because the guest is the side that needs something.
func (m *machine) serveCredentials(store credbroker.Store) error {
	device, err := m.socketDevice()
	if err != nil {
		return err
	}
	listener, err := device.Listen(credbroker.Port)
	if err != nil {
		return fmt.Errorf("applevf: listen for the guest credential broker on vsock port %d: %w", credbroker.Port, err)
	}
	m.credListener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener closed with the VM
			}
			go func() { _ = credbroker.Serve(conn, store) }()
		}
	}()
	return nil
}

// Shutdown asks the guest to power itself off and waits for it to go away.
//
// This exists because the Kata guest kernel has no gpio-keys driver, so Virtualization.framework's
// RequestStop is silently ignored inside the guest. Going through the control service also gives the
// guest a chance to hand back refreshed credentials before it halts.
func (m *machine) Shutdown() error {
	device, err := m.socketDevice()
	if err != nil {
		return err
	}
	conn, err := device.Connect(guestctl.Port)
	if err != nil {
		return fmt.Errorf("applevf: reach the guest control service: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(shutdownTimeout)); err != nil {
		return err
	}
	if err := guestctl.Send(conn, guestctl.CmdShutdown); err != nil {
		return err
	}
	return m.awaitStopped()
}

// awaitStopped waits for the VM to reach a stopped state after a shutdown request.
func (m *machine) awaitStopped() error {
	deadline := time.After(shutdownTimeout)
	states := m.vm.StateChangedNotify()
	for {
		if state := m.vm.State(); state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
			return nil
		}
		select {
		case <-states:
		case <-deadline:
			return fmt.Errorf("applevf: the guest did not halt within %s", shutdownTimeout)
		}
	}
}

// Ping reports whether the guest's control service is answering, which is a cheap liveness check that
// does not depend on the agent or the network being healthy.
func (m *machine) Ping() error {
	device, err := m.socketDevice()
	if err != nil {
		return err
	}
	conn, err := device.Connect(guestctl.Port)
	if err != nil {
		return fmt.Errorf("applevf: reach the guest control service: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := guestctl.Send(conn, guestctl.CmdPing); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
