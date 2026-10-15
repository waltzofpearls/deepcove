// Package localpeer tells whether a local TCP connection comes from the OS user this process runs as.
// The local dashboard uses it so that another user on the same machine cannot use deepcove, with no
// token for anyone to steal.
package localpeer

import (
	"errors"
	"net"
	"net/netip"
)

// ErrUnsupported is returned where this check is not built, so the caller refuses rather than guesses.
var ErrUnsupported = errors.New("localpeer: telling local users apart is not supported here")

// SameUser reports whether the far end of conn, a connection accepted on this machine, is owned by the
// user this process runs as. An error means it could not tell, which callers treat as no.
func SameUser(conn net.Conn) (bool, error) {
	server, err := addrPort(conn.LocalAddr())
	if err != nil {
		return false, err
	}
	client, err := addrPort(conn.RemoteAddr())
	if err != nil {
		return false, err
	}
	return sameUser(client, server)
}

func addrPort(addr net.Addr) (netip.AddrPort, error) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, errors.New("localpeer: not a TCP connection")
	}
	return netip.AddrPortFrom(netip.AddrFrom16([16]byte(tcp.IP.To16())).Unmap(), uint16(tcp.Port)), nil
}
