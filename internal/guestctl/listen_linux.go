//go:build linux

package guestctl

import (
	"context"
	"fmt"
	"net"

	"github.com/mdlayher/vsock"

	"github.com/waltzofpearls/deepcove/internal/guestserve"
)

// ListenAndServe runs the guest side control service, serving one command per connection until ctx ends.
func ListenAndServe(ctx context.Context, h Handler, logf func(string, ...any)) error {
	listener, err := vsock.Listen(Port, nil)
	if err != nil {
		return fmt.Errorf("guestctl: listen on vsock port %d: %w", Port, err)
	}
	defer func() { _ = listener.Close() }()

	return guestserve.Accept(ctx, listener, logf, func(conn net.Conn) { _ = h.Serve(conn) })
}
