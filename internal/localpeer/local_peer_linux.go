//go:build linux

package localpeer

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// sameUser finds the client's socket in the kernel's table and compares its owner. The kernel records
// the uid of whoever made each socket, so this needs no privileges and trusts nothing the client says.
func sameUser(client, server netip.AddrPort) (bool, error) {
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(table)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("localpeer: %w", err)
		}
		uid, found, err := socketOwner(file, client, server)
		_ = file.Close()
		if err != nil {
			return false, err
		}
		if found {
			return uid == os.Getuid(), nil
		}
	}
	return false, errors.New("localpeer: the client's socket is not in the kernel's table")
}

// socketOwner reads one /proc/net/tcp table for the socket whose own end is client and far end server.
func socketOwner(table io.Reader, client, server netip.AddrPort) (uid int, found bool, err error) {
	scan := bufio.NewScanner(table)
	scan.Scan() // the header
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) < 10 {
			continue
		}
		local, localErr := parseProcAddrPort(fields[1])
		remote, remoteErr := parseProcAddrPort(fields[2])
		if localErr != nil || remoteErr != nil || local != client || remote != server {
			continue
		}
		// A client that closed before deepcove took the connection leaves a row with no inode, and the
		// kernel shows its uid as 0. That 0 says nothing about who made it.
		if fields[9] == "0" {
			return 0, false, nil
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil {
			return 0, false, fmt.Errorf("localpeer: socket owner %q: %w", fields[7], err)
		}
		return uid, true, nil
	}
	return 0, false, scan.Err()
}

// parseProcAddrPort reads an address the way /proc/net/tcp writes it: the address in hex, in host order
// one 32 bit word at a time, then a colon and the port in hex.
func parseProcAddrPort(field string) (netip.AddrPort, error) {
	addrHex, portHex, ok := strings.Cut(field, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("localpeer: address %q", field)
	}
	raw, err := hex.DecodeString(addrHex)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, fmt.Errorf("localpeer: address %q", field)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("localpeer: port %q", field)
	}
	// Every machine deepcove runs on is little endian, so each word's bytes come reversed.
	for word := 0; word < len(raw); word += 4 {
		binary.BigEndian.PutUint32(raw[word:], binary.LittleEndian.Uint32(raw[word:]))
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), nil
}
