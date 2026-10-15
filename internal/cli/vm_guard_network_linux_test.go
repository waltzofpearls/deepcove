//go:build linux

package cli

import (
	"bufio"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseGuardNetworkArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantBlocked []netip.Prefix
		wantAllowed []string
		wantControl string
		wantCommand []string
		wantErr     bool
	}{
		{
			name: "blocks, allows, then passt",
			args: []string{
				"--block", "2001:db8:5::1/64", "--allow", "100.95.206.36", "--control", "/run/vm/guard.sock",
				"--", "/opt/passt", "--quiet",
			},
			wantBlocked: []netip.Prefix{netip.MustParsePrefix("2001:db8:5::/64")},
			wantAllowed: []string{"100.95.206.36"},
			wantControl: "/run/vm/guard.sock",
			wantCommand: []string{"/opt/passt", "--quiet"},
		},
		{name: "nothing to run", args: []string{"--control", "/run/vm/guard.sock"}, wantErr: true},
		{name: "no control socket", args: []string{"--", "/opt/passt"}, wantErr: true},
		{name: "not a network", args: []string{"--block", "nope", "--control", "/c", "--", "/opt/passt"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGuardNetworkArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGuardNetworkArgs: %v", err)
			}
			if !slices.Equal(got.blocked, tc.wantBlocked) || !slices.Equal(got.allowed, tc.wantAllowed) ||
				got.control != tc.wantControl || !slices.Equal(got.command, tc.wantCommand) {
				t.Errorf("got %+v, want blocked %v, allowed %v, control %s, command %v",
					got, tc.wantBlocked, tc.wantAllowed, tc.wantControl, tc.wantCommand)
			}
		})
	}
}

func TestReadBlockRequest(t *testing.T) {
	cases := []struct {
		name    string
		request string
		want    []netip.Prefix
		wantErr bool
	}{
		{
			name:    "one network per line",
			request: "203.0.113.5/24\n2001:db8:5::/64\n",
			want:    []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8:5::/64")},
		},
		{name: "blank lines skipped", request: "\n198.51.100.0/24\n\n", want: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}},
		{name: "not a network", request: "open everything\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			go func() {
				_, _ = client.Write([]byte(tc.request))
				_ = client.Close()
			}()
			got, err := readBlockRequest(server)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want an error", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("readBlockRequest = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestHandleControl(t *testing.T) {
	cases := []struct {
		name      string
		request   string
		blockErr  error
		wantReply string
		wantHeld  bool
		wantLost  bool
		wantBlock []netip.Prefix
	}{
		{name: "deepcove holds, then goes away", request: "hold\n", wantHeld: true, wantLost: true},
		{
			name:      "a network to block",
			request:   "203.0.113.5/24\n",
			wantReply: "ok",
			wantBlock: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
		},
		{
			name:      "the block fails",
			request:   "203.0.113.5/24\n",
			blockErr:  errors.New("no netlink"),
			wantReply: "error: no netlink",
			wantBlock: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := filepath.Join(t.TempDir(), "guard.sock")
			listener, err := net.Listen("unix", control)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			var blocked []netip.Prefix
			var held, lost bool
			handled := make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				block := func(networks []netip.Prefix) error {
					blocked = networks
					return tc.blockErr
				}
				handleControl(conn, block, func() { held = true }, func() { lost = true })
				close(handled)
			}()

			conn, err := net.Dial("unix", control)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write([]byte(tc.request)); err != nil {
				t.Fatal(err)
			}
			_ = conn.(*net.UnixConn).CloseWrite()
			reply, _ := bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			<-handled

			if strings.TrimSpace(reply) != tc.wantReply || held != tc.wantHeld || lost != tc.wantLost ||
				!slices.Equal(blocked, tc.wantBlock) {
				t.Errorf("reply %q, held %v, lost %v, blocked %v; want %q, %v, %v, %v",
					reply, held, lost, blocked, tc.wantReply, tc.wantHeld, tc.wantLost, tc.wantBlock)
			}
		})
	}
}

func TestServeControl(t *testing.T) {
	cases := []struct {
		name string
		// act runs against the control socket once the server is up.
		act      func(t *testing.T, control string, listener net.Listener)
		wantLost bool
	}{
		{
			name:     "no hold arrives in time",
			act:      func(*testing.T, string, net.Listener) {},
			wantLost: true,
		},
		{
			name: "a hold keeps the network up",
			act: func(t *testing.T, control string, _ net.Listener) {
				hold := dialHold(t, control)
				t.Cleanup(func() { _ = hold.Close() })
			},
		},
		{
			name: "the hold drops",
			act: func(t *testing.T, control string, _ net.Listener) {
				_ = dialHold(t, control).Close()
			},
			wantLost: true,
		},
		{
			name: "the control socket fails",
			act: func(t *testing.T, control string, listener net.Listener) {
				hold := dialHold(t, control)
				t.Cleanup(func() { _ = hold.Close() })
				_ = listener.Close()
			},
			wantLost: true,
		},
	}
	const holdWait = 200 * time.Millisecond
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := filepath.Join(t.TempDir(), "guard.sock")
			listener, err := net.Listen("unix", control)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			lost := make(chan struct{}, 4)
			block := func([]netip.Prefix) error { return nil }
			go serveControl(listener, block, func() { lost <- struct{}{} }, holdWait)

			tc.act(t, control, listener)

			// Long enough for the no hold timer to have fired had it not been stopped.
			select {
			case <-lost:
				if !tc.wantLost {
					t.Error("passt was stopped while deepcove was still watching")
				}
			case <-time.After(4 * holdWait):
				if tc.wantLost {
					t.Error("passt was never stopped")
				}
			}
		})
	}
}

// dialHold connects to the control socket as deepcove does when it starts watching.
func dialHold(t *testing.T, control string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", control)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(holdRequest + "\n")); err != nil {
		t.Fatal(err)
	}
	return conn
}
