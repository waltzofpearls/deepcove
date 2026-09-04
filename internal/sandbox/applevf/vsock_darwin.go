//go:build darwin

package applevf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// errGuestSilent is returned in place of dialing a machine whose recent dials went unanswered.
var errGuestSilent = errors.New("applevf: the guest is not answering on vsock")

// connect dials one of the guest's vsock services.
//
// It refuses when the VM is not running, and when recent dials went unanswered, and it gives up when
// ctx does.
func (m *machine) connect(ctx context.Context, port uint32) (*vz.VirtioSocketConnection, error) {
	if !m.dial.allow(time.Now()) {
		return nil, errGuestSilent
	}
	conn, err := m.probe(ctx, port)
	if err != nil {
		if ctx.Err() != nil {
			holdingOff := m.dial.recordMissed(time.Now())
			if holdingOff && m.logger != nil {
				m.logger.Warn("the guest stopped answering on vsock", "port", port)
			}
		}
		return nil, err
	}
	m.dial.recordAnswered()
	return conn, nil
}

// probe dials a guest vsock port without consulting the breaker, for the boot readiness poll.
//
// A guest that has not come up yet has not stopped answering, and counting its silence would hold off
// the very polls waiting for it: three missed dials arm a 30 second hold off, well inside the 20 second
// boot budget, and the VM would be destroyed as failed while it was still starting.
func (m *machine) probe(ctx context.Context, port uint32) (*vz.VirtioSocketConnection, error) {
	if state := m.vm.State(); state != vz.VirtualMachineStateRunning {
		return nil, fmt.Errorf("applevf: the vm is %s", state)
	}
	device, err := m.socketDevice()
	if err != nil {
		return nil, err
	}
	return dialGuest(ctx, device, port)
}

// dialGuest connects to a guest vsock port and abandons the dial when ctx is done.
//
// Virtualization.framework connects asynchronously, and Apple documents that connecting to a port
// nothing listens on "does nothing": the completion handler never runs, so the vz call blocks forever.
// The abandoned dial keeps running and closes whatever it eventually gets.
func dialGuest(ctx context.Context, device *vz.VirtioSocketDevice, port uint32) (*vz.VirtioSocketConnection, error) {
	type dialed struct {
		conn *vz.VirtioSocketConnection
		err  error
	}
	done := make(chan dialed, 1)
	go func() {
		conn, err := device.Connect(port)
		done <- dialed{conn, err}
	}()
	select {
	case d := <-done:
		return d.conn, d.err
	case <-ctx.Done():
		go func() {
			if d := <-done; d.conn != nil {
				_ = d.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// unresponsive reports whether dials to this guest are being held off.
func (m *machine) unresponsive() bool { return !m.dial.allow(time.Now()) }
