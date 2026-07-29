// Package guestctl is the small control channel between the host and a session VM, alongside the exec
// service. It exists mainly for graceful shutdown: the Kata guest kernel has no gpio-keys driver, so
// Apple VF's RequestStop is ignored inside the guest and the host has no other way to ask it to halt.
//
// Like guestexec, the wire format is deliberately tiny and both ends work on a plain net.Conn, so the
// protocol is testable without a VM.
package guestctl

import (
	"errors"
	"fmt"
	"io"
	"net"
)

// Port is the vsock port the guest's control service listens on, one above the exec service.
const Port uint32 = 1025

// Command is a single byte request from the host.
type Command byte

const (
	// CmdShutdown asks the guest to flush and power off.
	CmdShutdown Command = 1
	// CmdPing checks the guest is alive and its control service is answering.
	CmdPing Command = 2
)

// Reply is the guest's single byte answer.
type Reply byte

const (
	ReplyOK    Reply = 1
	ReplyError Reply = 2
)

// ErrGuestRefused means the guest received the command but could not carry it out.
var ErrGuestRefused = errors.New("guestctl: the guest refused the command")

// Send issues one command and waits for the reply. Shutdown is a special case: the guest powers off
// rather than answering, so a closed connection counts as success.
func Send(conn net.Conn, cmd Command) error {
	if _, err := conn.Write([]byte{byte(cmd)}); err != nil {
		return fmt.Errorf("guestctl: send command: %w", err)
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		if cmd == CmdShutdown && isClosed(err) {
			// The machine went away mid reply, which is exactly what shutdown is supposed to do.
			return nil
		}
		return fmt.Errorf("guestctl: read reply: %w", err)
	}
	if Reply(reply[0]) != ReplyOK {
		return ErrGuestRefused
	}
	return nil
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

// Handler carries out a command in the guest. Shutdown powers the machine off, so it normally never
// returns.
type Handler struct {
	Shutdown func() error
}

// Serve reads one command from conn and runs it.
func (h Handler) Serve(conn net.Conn) error {
	defer func() { _ = conn.Close() }()

	var buf [1]byte
	if _, err := io.ReadFull(conn, buf[:]); err != nil {
		return fmt.Errorf("guestctl: read command: %w", err)
	}
	switch Command(buf[0]) {
	case CmdPing:
		return reply(conn, ReplyOK)
	case CmdShutdown:
		if h.Shutdown == nil {
			return reply(conn, ReplyError)
		}
		// Answer before halting; once poweroff runs there is no connection left to answer on.
		_ = reply(conn, ReplyOK)
		return h.Shutdown()
	default:
		return reply(conn, ReplyError)
	}
}

func reply(conn net.Conn, r Reply) error {
	_, err := conn.Write([]byte{byte(r)})
	return err
}
