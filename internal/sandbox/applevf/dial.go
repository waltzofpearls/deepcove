package applevf

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
)

// maxPendingDials bounds what a guest that stopped listening can cost. Apple documents that connecting
// to a port nothing listens on "does nothing": that call never returns, so its goroutine is gone for the
// life of the process. Once this many are outstanding the guest is plainly not accepting, and further
// dials are refused rather than added to the pile.
const maxPendingDials = 8

// connectFunc opens a connection to a guest port. It is the one hypervisor call the dial path makes, and
// it is a field so the rest can be tested without a hypervisor.
type connectFunc func(port uint32) (net.Conn, error)

// dialer opens guest connections and keeps count of the ones that never came back.
type dialer struct {
	connect connectFunc
	logger  *slog.Logger
	pending atomic.Int32
	// reported makes the warning fire once per machine rather than once per refused dial.
	reported atomic.Bool
}

func newDialer(connect connectFunc, logger *slog.Logger) *dialer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &dialer{connect: connect, logger: logger}
}

// Pending is how many dials have not returned, which is a direct measure of a guest that is not
// listening.
func (d *dialer) Pending() int {
	return int(d.pending.Load())
}

// Dial connects to a guest port and abandons the dial when ctx is done. An abandoned dial closes
// whatever it eventually gets, if it ever does.
func (d *dialer) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	if pending := d.pending.Load(); pending >= maxPendingDials {
		if d.reported.CompareAndSwap(false, true) {
			d.logger.Warn("the guest is not listening on vsock, refusing further dials",
				"port", port, "pending", pending)
		}
		return nil, fmt.Errorf("applevf: %d dials to this guest have not returned, it is not listening on vsock", pending)
	}

	type dialed struct {
		conn net.Conn
		err  error
	}
	done := make(chan dialed, 1)
	d.pending.Add(1)
	go func() {
		defer d.pending.Add(-1)
		conn, err := d.connect(port)
		if ctx.Err() != nil {
			// Nobody is waiting for this any more.
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		done <- dialed{conn, err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			return nil, result.err
		}
		return result.conn, nil
	case <-ctx.Done():
		// The dial may have landed in the moment the context expired.
		select {
		case result := <-done:
			if result.conn != nil {
				_ = result.conn.Close()
			}
		default:
		}
		return nil, ctx.Err()
	}
}
