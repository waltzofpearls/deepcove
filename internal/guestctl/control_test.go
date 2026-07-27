package guestctl

import (
	"errors"
	"net"
	"testing"
	"time"
)

// serveOne wires a host conn to a Handler over an in memory pipe.
func serveOne(t *testing.T, h Handler) net.Conn {
	t.Helper()
	hostConn, guestConn := net.Pipe()
	go func() { _ = h.Serve(guestConn) }()
	t.Cleanup(func() { _ = hostConn.Close() })
	return hostConn
}

func TestPing(t *testing.T) {
	conn := serveOne(t, Handler{})
	if err := Send(conn, CmdPing); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestShutdownInvokesTheHandler(t *testing.T) {
	called := make(chan struct{}, 1)
	conn := serveOne(t, Handler{
		Shutdown: func() error {
			called <- struct{}{}
			return nil
		},
	})

	if err := Send(conn, CmdShutdown); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Error("the shutdown handler was never invoked")
	}
}

// A guest with no shutdown handler must refuse rather than silently appear to succeed.
func TestShutdownWithoutHandlerIsRefused(t *testing.T) {
	conn := serveOne(t, Handler{})
	if err := Send(conn, CmdShutdown); !errors.Is(err, ErrGuestRefused) {
		t.Errorf("error = %v, want ErrGuestRefused", err)
	}
}

func TestUnknownCommandIsRefused(t *testing.T) {
	conn := serveOne(t, Handler{})
	if err := Send(conn, Command(99)); !errors.Is(err, ErrGuestRefused) {
		t.Errorf("error = %v, want ErrGuestRefused", err)
	}
}

// A machine that powers off mid reply is shutdown working, not a failure, so a closed connection has to
// count as success for that one command.
func TestShutdownTreatsAClosedConnectionAsSuccess(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	go func() {
		var buf [1]byte
		_, _ = guestConn.Read(buf[:])
		_ = guestConn.Close() // vanish without replying, as a real poweroff would
	}()
	defer func() { _ = hostConn.Close() }()

	if err := Send(hostConn, CmdShutdown); err != nil {
		t.Errorf("a closed connection during shutdown should be success, got %v", err)
	}
}

// The same closed connection during a ping is a genuine failure: the guest is gone unexpectedly.
func TestPingReportsAClosedConnection(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	go func() {
		var buf [1]byte
		_, _ = guestConn.Read(buf[:])
		_ = guestConn.Close()
	}()
	defer func() { _ = hostConn.Close() }()

	if err := Send(hostConn, CmdPing); err == nil {
		t.Error("a ping against a vanished guest should fail")
	}
}
