// Package guestserve holds the accept loop the guest's vsock services share.
package guestserve

import (
	"context"
	"errors"
	"net"
	"time"
)

// Backoff between failed accepts, so a listener that is broken for a while does not spin.
const (
	minAcceptBackoff = 50 * time.Millisecond
	maxAcceptBackoff = 2 * time.Second
)

// Accept serves connections until ctx ends or the listener is closed. serve owns the connection it is
// given, including closing it.
//
// A failed accept is retried rather than returned. These services are the only way into a VM, so ending
// the loop on one error leaves a machine nobody can reach or shut down. A panic in one connection is
// contained for the same reason: this runs as PID 1, where a panic panics the kernel.
func Accept(ctx context.Context, listener net.Listener, logf func(string, ...any), serve func(net.Conn)) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	backoff := minAcceptBackoff
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			logf("accept failed, retrying in %s: %v", backoff, err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, maxAcceptBackoff)
			continue
		}
		backoff = minAcceptBackoff
		go func() {
			defer func() {
				// Only logging here. A panic raised inside this function would escape the recover and take
				// the process down, which is the failure the recover exists to prevent.
				if r := recover(); r != nil {
					logf("a connection panicked: %v", r)
				}
			}()
			serve(conn)
		}()
	}
}
