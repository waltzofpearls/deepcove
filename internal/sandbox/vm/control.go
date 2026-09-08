package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/waltzofpearls/deepcove/internal/guestctl"
)

// controlDialTimeout bounds the dial to the guest's control service. A guest that stopped answering
// then falls through to stopping the VM outright instead of holding up the process.
const controlDialTimeout = 5 * time.Second

// shutdownTimeout bounds both halves of a polite shutdown: asking the guest, and waiting for it to go.
const shutdownTimeout = 10 * time.Second

// Shutdown asks the guest to power itself off and waits for it to go away.
//
// This goes through the control service rather than the hypervisor's own stop request: the Kata guest
// kernel has no gpio-keys driver, so a virtual power button is silently ignored inside the guest. It
// also gives the guest a chance to hand back refreshed credentials before it halts.
func (g *Guest) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), controlDialTimeout)
	defer cancel()
	conn, err := g.connect(ctx, guestctl.Port)
	if err != nil {
		return fmt.Errorf("vm: reach the guest control service: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(shutdownTimeout)); err != nil {
		return err
	}
	if err := guestctl.Send(conn, guestctl.CmdShutdown); err != nil {
		return err
	}
	return g.machine.WaitStopped(shutdownTimeout)
}

// Ping reports whether the guest's control service is answering, which is a cheap liveness check that
// does not depend on the agent or the network being healthy.
func (g *Guest) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), controlDialTimeout)
	defer cancel()
	// Not through the breaker: this is the boot poll, and a guest still coming up must not hold itself
	// off before it has had a chance to answer.
	conn, err := g.probe(ctx, guestctl.Port)
	if err != nil {
		return fmt.Errorf("vm: reach the guest control service: %w", err)
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
