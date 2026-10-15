//go:build linux

package localpeer

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseProcAddrPort(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"0100007F:2553", "127.0.0.1:9555"},
		{"00000000000000000000000001000000:2328", "[::1]:9000"},
		{"0000000000000000FFFF00000100007F:9B90", "127.0.0.1:39824"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			got, err := parseProcAddrPort(tc.field)
			if err != nil || got != netip.MustParseAddrPort(tc.want) {
				t.Errorf("parseProcAddrPort = %v, %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestSocketOwner(t *testing.T) {
	// Real /proc/net/tcp lines: the server's listener, two clients of two users with the server's end
	// of each, and a client that closed before deepcove took its connection, which the kernel shows as
	// uid 0 with no inode. Reading the server's end by mistake would answer 1000 for the 1001 client.
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2553 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1
   1: 0100007F:9B84 0100007F:2553 01 00000000:00000000 00:00000000 00000000  1000        0 2 1
   2: 0100007F:9B90 0100007F:2553 01 00000000:00000000 00:00000000 00000000  1001        0 3 1
   3: 0100007F:2553 0100007F:9B84 01 00000000:00000000 00:00000000 00000000  1000        0 4 1
   4: 0100007F:2553 0100007F:9B90 01 00000000:00000000 00:00000000 00000000  1000        0 5 1
   5: 0100007F:9B9A 0100007F:2553 05 00000000:00000000 03:0000175F 00000000     0        0 0 3
`
	server := netip.MustParseAddrPort("127.0.0.1:9555")
	cases := []struct {
		name      string
		client    string
		wantUID   int
		wantFound bool
	}{
		{"one user's client", "127.0.0.1:39812", 1000, true},
		{"another user's client", "127.0.0.1:39824", 1001, true},
		{"a client that is gone", "127.0.0.1:40000", 0, false},
		{"a client that closed first", "127.0.0.1:39834", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid, found, err := socketOwner(strings.NewReader(table), netip.MustParseAddrPort(tc.client), server)
			if err != nil || uid != tc.wantUID || found != tc.wantFound {
				t.Errorf("socketOwner = %d, %v, %v, want %d, %v", uid, found, err, tc.wantUID, tc.wantFound)
			}
		})
	}
}
