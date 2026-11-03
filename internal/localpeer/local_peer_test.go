package localpeer

import (
	"net"
	"net/netip"
	"testing"
)

func TestSameUser_OurOwnConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	same, err := SameUser(server)
	if err != nil || !same {
		t.Errorf("SameUser = %v, %v, want true for a connection this process made", same, err)
	}
}

// No socket of ours has both of these ends, which is what a connection from another user looks like.
func TestSameUser_NoSocketOfOurs(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ourClient := netip.MustParseAddrPort(client.LocalAddr().String())
	ourServer := netip.MustParseAddrPort(listener.Addr().String())

	cases := []struct {
		name   string
		client netip.AddrPort
		server netip.AddrPort
	}{
		{"a client end nobody of ours holds", netip.MustParseAddrPort("127.0.0.1:1"), ourServer},
		{"our client end, to another server address", ourClient, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), ourServer.Port())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if same, _ := sameUser(tc.client, tc.server); same {
				t.Error("sameUser = true for a socket no process of ours has")
			}
		})
	}
}
