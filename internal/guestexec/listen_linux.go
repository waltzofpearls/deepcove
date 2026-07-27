//go:build linux

package guestexec

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/mdlayher/vsock"
)

// ListenAndServe runs the guest side exec service: it accepts vsock connections from the host and serves
// one exec per connection. It blocks until ctx is cancelled or the listener fails, so the guest init
// starts it in its own goroutine.
func ListenAndServe(ctx context.Context) error {
	listener, err := vsock.Listen(Port, nil)
	if err != nil {
		return fmt.Errorf("guestexec: listen on vsock port %d: %w", Port, err)
	}
	defer func() { _ = listener.Close() }()

	// Closing the listener is what unblocks Accept when the context ends.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil // shutting down, not a failure
			}
			return fmt.Errorf("guestexec: accept: %w", err)
		}
		// One exec per connection, each independent, so a slow command never blocks the next.
		go func() { _ = Serve(conn) }()
	}
}
