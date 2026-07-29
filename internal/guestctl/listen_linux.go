//go:build linux

package guestctl

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/mdlayher/vsock"
)

// ListenAndServe runs the guest side control service, serving one command per connection until ctx ends.
func ListenAndServe(ctx context.Context, h Handler) error {
	listener, err := vsock.Listen(Port, nil)
	if err != nil {
		return fmt.Errorf("guestctl: listen on vsock port %d: %w", Port, err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("guestctl: accept: %w", err)
		}
		go func() { _ = h.Serve(conn) }()
	}
}
