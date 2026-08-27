//go:build linux

package guestexec

import (
	"context"
	"fmt"
	"net"

	"github.com/mdlayher/vsock"

	"github.com/waltzofpearls/deepcove/internal/guestserve"
)

// ListenAndServe runs the guest side exec service: it accepts vsock connections from the host and serves
// one exec per connection. It blocks until ctx is cancelled or the listener cannot be created, so the
// guest init starts it in its own goroutine.
func ListenAndServe(ctx context.Context, waiter ChildWaiter, logf func(string, ...any)) error {
	listener, err := vsock.Listen(Port, nil)
	if err != nil {
		return fmt.Errorf("guestexec: listen on vsock port %d: %w", Port, err)
	}
	defer func() { _ = listener.Close() }()

	// One exec per connection, each independent, so a slow command never blocks the next.
	return guestserve.Accept(ctx, listener, logf, func(conn net.Conn) { _ = serveWith(conn, waiter) })
}
