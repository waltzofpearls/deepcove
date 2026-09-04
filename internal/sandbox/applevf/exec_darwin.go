//go:build darwin

package applevf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// execDialTimeout bounds how long we wait for the guest's exec service to accept a connection. The
// service only starts once the guest init has finished booting, so an exec issued moments after Start
// can arrive first and needs a retry window rather than an immediate failure.
const execDialTimeout = 15 * time.Second

// execDialRetry is how long to wait between connection attempts while the guest is still coming up.
const execDialRetry = 200 * time.Millisecond

// Exec runs a command inside the VM and returns it as a sandbox.Process, the same interface the Docker
// driver returns, so the session layer does not care which runtime is underneath.
func (m *machine) Exec(ctx context.Context, spec guestexec.ExecRequest) (sandbox.Process, error) {
	conn, err := m.dialExec(ctx)
	if err != nil {
		return nil, err
	}
	client, err := guestexec.Start(conn, spec)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

// dialExec connects to the guest's exec service over vsock, retrying while the guest finishes booting.
//
// It honours ctx so a caller's deadline actually means something. Without that a short lived call (the
// bootstrap poll allows 3s) could block here for the full retry window, time out upstream, and be
// reported as a failure the caller then papers over with a default.
func (m *machine) dialExec(ctx context.Context) (*vz.VirtioSocketConnection, error) {
	dialCtx, cancel := context.WithTimeout(ctx, execDialTimeout)
	defer cancel()
	for {
		conn, err := m.connect(dialCtx, guestexec.Port)
		if err == nil {
			return conn, nil
		}
		if dialCtx.Err() != nil || errors.Is(err, errGuestSilent) {
			return nil, fmt.Errorf("applevf: connect to the guest exec service on vsock port %d: %w", guestexec.Port, err)
		}
		select {
		case <-dialCtx.Done():
			return nil, dialCtx.Err()
		case <-time.After(execDialRetry):
		}
	}
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
