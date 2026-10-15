package cloudhv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPastaArgs(t *testing.T) {
	guard := guardNetworkArgs([]string{"100.95.206.36"}, []netip.Prefix{netip.MustParsePrefix("2001:db8:5::/64")}, "/run/vm/guard.sock")
	args := pastaArgs("/bin/deepcove", []string{"--runas", "0:0"}, guard, "/opt/passt", []string{"--socket", "/run/net.sock"})
	// Each of these carries the guest back to the host if it is left out.
	for _, flag := range []string{"--no-map-gw", "--config-net"} {
		if !slices.Contains(args, flag) {
			t.Errorf("args = %v, want %s", args, flag)
		}
	}
	for _, flag := range []struct {
		name string
		want []string
	}{
		{name: "-T", want: []string{"none"}},
		{name: "-U", want: []string{"none"}},
		{name: "-a", want: []string{pastaAddress}},
		{name: "--dns-forward", want: []string{pastaDNS}},
		{name: "--block", want: []string{"2001:db8:5::/64"}},
		{name: "--allow", want: []string{"100.95.206.36"}},
		{name: "--control", want: []string{"/run/vm/guard.sock"}},
		{name: "--runas", want: []string{"0:0"}},
	} {
		if got := valuesOf(args, flag.name); !slices.Equal(got, flag.want) {
			t.Errorf("%s = %v, want %v", flag.name, got, flag.want)
		}
	}
	command := args[slices.Index(args, "--")+1:]
	if !slices.Equal(command[:3], []string{"/bin/deepcove", "vm", "guard-network"}) {
		t.Errorf("pasta runs %v, want deepcove's guard-network first", command)
	}
	passt := command[slices.Index(command, "--")+1:]
	if !slices.Equal(passt, []string{"/opt/passt", "--socket", "/run/net.sock"}) {
		t.Errorf("then %v, want passt with its own args", passt)
	}
}

func TestPasstArgs(t *testing.T) {
	args := passtArgs("/run/passt.sock", []string{"--runas", "1000"})
	if !slices.Contains(args, "--no-map-gw") {
		t.Errorf("args = %v, want --no-map-gw", args)
	}
	for _, flag := range []struct {
		name string
		want []string
	}{
		{name: "-a", want: []string{passtGuestAddress}},
		{name: "-n", want: []string{passtGuestPrefix}},
		{name: "-g", want: []string{passtGuestGateway}},
		{name: "-D", want: []string{passtDNSv4, passtDNSv6}},
		{name: "--dns-forward", want: []string{passtDNSv4, passtDNSv6}},
		{name: "--dns-host", want: []string{pastaDNS}},
	} {
		if got := valuesOf(args, flag.name); !slices.Equal(got, flag.want) {
			t.Errorf("%s = %v, want %v", flag.name, got, flag.want)
		}
	}
	if args[len(args)-1] != "1000" {
		t.Errorf("args = %v, want the runas flags kept at the end", args)
	}
}

// valuesOf returns every value that follows flag in args.
func valuesOf(args []string, flag string) []string {
	var values []string
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}

func TestUnsentNetworks(t *testing.T) {
	atBoot := netip.MustParsePrefix("146.190.112.0/20")
	joined := netip.MustParsePrefix("203.0.113.0/24")
	sent := map[netip.Prefix]bool{atBoot: true}
	if got := unsentNetworks([]netip.Prefix{atBoot, joined}, sent); !slices.Equal(got, []netip.Prefix{joined}) {
		t.Errorf("unsentNetworks = %v, want only the network the host joined", got)
	}
}

// fakeGuard answers on a control socket the way the VM's guard does, and records what it was sent.
type fakeGuard struct {
	reply      string
	mu         sync.Mutex
	requests   [][]netip.Prefix
	holdClosed chan struct{}
}

func startFakeGuard(t *testing.T, control, reply string) *fakeGuard {
	t.Helper()
	listener, err := net.Listen("unix", control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	guard := &fakeGuard{reply: reply, holdClosed: make(chan struct{})}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go guard.handle(conn)
		}
	}()
	return guard
}

func (g *fakeGuard) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	scan := bufio.NewScanner(conn)
	var networks []netip.Prefix
	for scan.Scan() {
		if scan.Text() == "hold" {
			for scan.Scan() {
			}
			close(g.holdClosed)
			return
		}
		networks = append(networks, netip.MustParsePrefix(scan.Text()))
	}
	g.mu.Lock()
	g.requests = append(g.requests, networks)
	g.mu.Unlock()
	_, _ = fmt.Fprintln(conn, g.reply)
}

func (g *fakeGuard) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func TestKeepHostNetworksBlocked(t *testing.T) {
	atBoot := netip.MustParsePrefix("146.190.112.0/20")
	joined := netip.MustParsePrefix("203.0.113.0/24")
	errNoInterfaces := errors.New("no interfaces")
	cases := []struct {
		name         string
		noGuard      bool
		reply        string
		readErrs     []error
		watchErr     error
		stopped      bool
		wantStartErr bool
		wantRequests int
		wantFailed   bool
	}{
		{name: "a joined network is sent once", reply: "ok", wantRequests: 1},
		{name: "the guard refuses it at start", reply: "error: no", wantStartErr: true, wantRequests: 1},
		{name: "the networks cannot be read at start", reply: "ok", readErrs: []error{errNoInterfaces}, wantStartErr: true},
		{name: "the watch cannot start", reply: "ok", watchErr: errors.New("no netlink"), wantStartErr: true},
		{name: "no guard to hold", noGuard: true, wantStartErr: true},
		{name: "a later read fails", reply: "ok", readErrs: []error{nil, errNoInterfaces}, wantRequests: 1, wantFailed: true},
		{name: "the VM stops, ending the watch", reply: "ok", stopped: true, wantRequests: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := filepath.Join(shortTempDir(t), "guard.sock")
			var guard *fakeGuard
			if !tc.noGuard {
				guard = startFakeGuard(t, control, tc.reply)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Two looks in a row: the second must not send a network the guard already took.
			watch := func(ctx context.Context, changed func()) error {
				if tc.watchErr != nil {
					return tc.watchErr
				}
				changed()
				if tc.stopped {
					cancel()
					return ctx.Err()
				}
				changed()
				return nil
			}
			reads := 0
			read := func() ([]netip.Prefix, error) {
				var err error
				if reads < len(tc.readErrs) {
					err = tc.readErrs[reads]
				}
				reads++
				return []netip.Prefix{atBoot, joined}, err
			}
			started := make(chan error, 1)
			var failed atomic.Bool
			keepHostNetworksBlocked(ctx, control, []netip.Prefix{atBoot}, func() { failed.Store(true) }, watch, read, started)

			if startErr := <-started; (startErr != nil) != tc.wantStartErr {
				t.Errorf("start error = %v, want one: %v", startErr, tc.wantStartErr)
			}
			if failed.Load() != tc.wantFailed {
				t.Errorf("failed closed = %v, want %v", failed.Load(), tc.wantFailed)
			}
			if guard == nil {
				return
			}
			if got := guard.requestCount(); got != tc.wantRequests {
				t.Errorf("guard got %d requests, want %d", got, tc.wantRequests)
			}
			// The hold ends when the watch does, which is what tells the guard deepcove is gone.
			select {
			case <-guard.holdClosed:
			case <-time.After(5 * time.Second):
				t.Error("the hold on the guard was never released")
			}
		})
	}
}
