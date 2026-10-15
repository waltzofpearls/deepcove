//go:build darwin

package localpeer

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// fakeTableRow builds one row of the kernel's TCP table with only the fields socketOwner reads.
func fakeTableRow(local, remote string, uid uint32) []byte {
	row := make([]byte, rowMinSize)
	binary.NativeEndian.PutUint32(row, rowMinSize)
	localEnd, remoteEnd := netip.MustParseAddrPort(local), netip.MustParseAddrPort(remote)
	row[rowIPVersionAt] = rowIsIPv6
	if localEnd.Addr().Is4() {
		row[rowIPVersionAt] = rowIsIPv4
	}
	localAddr, remoteAddr := localEnd.Addr().As16(), remoteEnd.Addr().As16()
	copy(row[rowLocalAddrAt:], localAddr[:])
	copy(row[rowRemoteAddrAt:], remoteAddr[:])
	binary.BigEndian.PutUint16(row[rowLocalPortAt:], localEnd.Port())
	binary.BigEndian.PutUint16(row[rowRemotePortAt:], remoteEnd.Port())
	binary.NativeEndian.PutUint32(row[rowOwnerUIDAt:], uid)
	return row
}

func TestSocketOwner(t *testing.T) {
	header := make([]byte, 24)
	binary.NativeEndian.PutUint32(header, 24)
	var table []byte
	table = append(table, header...)
	table = append(table, fakeTableRow("127.0.0.1:9000", "127.0.0.1:60513", 501)...)
	table = append(table, fakeTableRow("127.0.0.1:60513", "127.0.0.1:9000", 501)...)
	table = append(table, fakeTableRow("127.0.0.1:9000", "127.0.0.1:60520", 501)...)
	table = append(table, fakeTableRow("127.0.0.1:60520", "127.0.0.1:9000", 502)...)
	table = append(table, fakeTableRow("[::1]:60530", "[::1]:9000", 501)...)
	table = append(table, header...)

	cases := []struct {
		name      string
		client    string
		server    string
		wantUID   int
		wantFound bool
	}{
		{"one user's client", "127.0.0.1:60513", "127.0.0.1:9000", 501, true},
		{"another user's client", "127.0.0.1:60520", "127.0.0.1:9000", 502, true},
		{"an ipv6 client", "[::1]:60530", "[::1]:9000", 501, true},
		{"a client that is gone", "127.0.0.1:61000", "127.0.0.1:9000", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid, found := socketOwner(table, netip.MustParseAddrPort(tc.client), netip.MustParseAddrPort(tc.server))
			if uid != tc.wantUID || found != tc.wantFound {
				t.Errorf("socketOwner = %d, %v, want %d, %v", uid, found, tc.wantUID, tc.wantFound)
			}
		})
	}
}
