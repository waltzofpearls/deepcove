package vmproc

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestServeGuest(t *testing.T) {
	cases := []struct {
		name    string
		dial    DialFunc
		wantErr bool
	}{
		{name: "passes bytes to the guest and back", dial: echoGuest},
		{
			name:    "a guest port nobody answers is refused",
			dial:    func(context.Context, uint32) (net.Conn, error) { return nil, errors.New("no listener") },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(shortTempDir(t), "vsock.sock")
			relay, err := ServeGuest(socket, tc.dial)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = relay.Close() })

			conn, err := DialGuest(context.Background(), socket, 1024)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DialGuest error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			assertEchoes(t, conn)
		})
	}
}

func TestForwardFromGuest(t *testing.T) {
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "vsock.sock")
	host, err := ListenGuest(socket, 1026)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	go func() {
		conn, err := host.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(conn, conn)
	}()

	// A Unix listener stands in for the guest's vsock listener.
	guest, err := net.Listen("unix", filepath.Join(dir, "guest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guest.Close() }()
	go ForwardFromGuest(guest, socket, 1026)

	conn, err := net.Dial("unix", filepath.Join(dir, "guest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	assertEchoes(t, conn)
}

func TestForwardFromGuest_WaitsForTheHostSide(t *testing.T) {
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "vsock.sock")
	guest, err := net.Listen("unix", filepath.Join(dir, "guest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guest.Close() }()
	go ForwardFromGuest(guest, socket, 1027)

	// The guest calls first; the host side only starts listening a moment later.
	conn, err := net.Dial("unix", filepath.Join(dir, "guest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(200 * time.Millisecond)
	host, err := ListenGuest(socket, 1027)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	go func() {
		accepted, err := host.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(accepted, accepted)
	}()
	assertEchoes(t, conn)
}

// echoGuest is a guest port that sends back whatever it gets.
func echoGuest(context.Context, uint32) (net.Conn, error) {
	host, guest := net.Pipe()
	go func() { _, _ = io.Copy(guest, guest) }()
	return host, nil
}

func assertEchoes(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping" {
		t.Errorf("got %q, %v; want ping back", got, err)
	}
}
