package localpeer

import (
	"net"
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
