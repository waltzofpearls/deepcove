//go:build darwin

package localpeer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// Where the fields sit in one row of the kernel's TCP table, a struct xtcpcb64 from Apple's public
// headers <netinet/tcp_var.h> and <sys/socketvar.h>.
const (
	rowMinSize      = 472
	rowRemotePortAt = 20
	rowLocalPortAt  = 22
	rowIPVersionAt  = 96
	rowRemoteAddrAt = 100
	rowLocalAddrAt  = 116
	rowOwnerUIDAt   = 252
	rowIsIPv4       = 0x1
	rowIsIPv6       = 0x2
)

// sameUser finds the client's socket in the kernel's TCP table and compares its owner, the same way
// netstat reads it. The kernel records the uid of whoever made each socket, so this needs no
// privileges and trusts nothing the client says.
func sameUser(client, server netip.AddrPort) (bool, error) {
	table, err := unix.SysctlRaw("net.inet.tcp.pcblist64")
	if err != nil {
		return false, fmt.Errorf("localpeer: read the TCP table: %w", err)
	}
	uid, found := socketOwner(table, client, server)
	if !found {
		return false, errors.New("localpeer: the client's socket is not in the kernel's TCP table")
	}
	return uid == os.Getuid(), nil
}

// socketOwner reads the table for the socket whose own end is client and far end server. The table is
// a header, then one row per socket, then a closing header. Each starts with its own length.
func socketOwner(table []byte, client, server netip.AddrPort) (uid int, found bool) {
	if len(table) < 4 {
		return 0, false
	}
	offset := int(binary.NativeEndian.Uint32(table))
	for offset+rowMinSize <= len(table) {
		row := table[offset:]
		rowLen := int(binary.NativeEndian.Uint32(row))
		if rowLen < rowMinSize {
			break
		}
		local, localOK := rowEnd(row, rowLocalAddrAt, rowLocalPortAt)
		remote, remoteOK := rowEnd(row, rowRemoteAddrAt, rowRemotePortAt)
		if localOK && remoteOK && local == client && remote == server {
			return int(binary.NativeEndian.Uint32(row[rowOwnerUIDAt:])), true
		}
		offset += rowLen
	}
	return 0, false
}

// rowEnd reads one end of a row. Ports are in network order. An IPv4 address sits in the last four
// bytes of its 16 byte field.
func rowEnd(row []byte, addrAt, portAt int) (netip.AddrPort, bool) {
	port := binary.BigEndian.Uint16(row[portAt:])
	field := [16]byte(row[addrAt : addrAt+16])
	switch {
	case row[rowIPVersionAt]&rowIsIPv4 != 0:
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(field[12:])), port), true
	case row[rowIPVersionAt]&rowIsIPv6 != 0:
		return netip.AddrPortFrom(netip.AddrFrom16(field).Unmap(), port), true
	}
	return netip.AddrPort{}, false
}
