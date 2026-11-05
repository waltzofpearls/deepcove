package cli

import (
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/config"
)

// takePort listens on a free local port, standing in for another app, and returns its address.
func takePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return listener.Addr().String()
}

// serveOnSocket stands in for a running deepcove. An older one has no handler for listenAddrPath.
func serveOnSocket(t *testing.T, dataDir string, handler http.HandlerFunc) {
	t.Helper()
	socketMux := http.NewServeMux()
	if handler != nil {
		socketMux.HandleFunc("GET "+listenAddrPath, handler)
	}
	server, listener, err := listenUnixSocket(socketPath(dataDir), socketMux)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})
}

func TestIsPortInUseError(t *testing.T) {
	takenAddr := takePort(t)
	_, inUseErr := net.Listen("tcp", takenAddr)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"a taken port", inUseErr, true},
		{"no error", nil, false},
		{"some other error", errors.New("permission denied"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPortInUseError(tc.err); got != tc.want {
				t.Errorf("isPortInUseError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFindNextFreeAddr(t *testing.T) {
	takenAddr := takePort(t)
	freeAddr, found := findNextFreeAddr(takenAddr)
	if !found {
		t.Fatalf("no free port after %s", takenAddr)
	}
	if freeAddr == takenAddr {
		t.Errorf("findNextFreeAddr(%s) returned the taken port", takenAddr)
	}
	listener, err := net.Listen("tcp", freeAddr)
	if err != nil {
		t.Fatalf("%s should be free: %v", freeAddr, err)
	}
	_ = listener.Close()
}

func TestFindAddrFlagIn(t *testing.T) {
	cases := []struct {
		name      string
		startArgs []string
		wantAddr  string
		wantFound bool
	}{
		{name: "none", startArgs: []string{"/usr/local/bin/deepcove", "start"}},
		{name: "--addr then a value", startArgs: []string{"start", "--addr", "127.0.0.1:9090"}, wantAddr: "127.0.0.1:9090", wantFound: true},
		{name: "--addr=", startArgs: []string{"start", "--addr=127.0.0.1:9090"}, wantAddr: "127.0.0.1:9090", wantFound: true},
		{name: "-addr", startArgs: []string{"start", "-addr", "127.0.0.1:9090"}, wantAddr: "127.0.0.1:9090", wantFound: true},
		{name: "--addr with no value", startArgs: []string{"start", "--addr"}},
		{name: "--addr twice, the last wins", startArgs: []string{"start", "--addr=127.0.0.1:9000", "--addr", "127.0.0.1:9001"}, wantAddr: "127.0.0.1:9001", wantFound: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, found := findAddrFlagIn(tc.startArgs)
			if addr != tc.wantAddr || found != tc.wantFound {
				t.Errorf("got %q, %v, want %q, %v", addr, found, tc.wantAddr, tc.wantFound)
			}
		})
	}
}

func TestBuildStartArgsWithAddr(t *testing.T) {
	cases := []struct {
		name         string
		startCommand []string
		want         []string
	}{
		{
			name:         "only --addr",
			startCommand: []string{"/usr/local/bin/deepcove", "start", "--addr", "127.0.0.1:9000"},
			want:         []string{"--addr", "127.0.0.1:9001"},
		},
		{
			name:         "other flags stay",
			startCommand: []string{"/usr/local/bin/deepcove", "start", "--sandbox-can-reach", "10.0.0.5", "--addr=127.0.0.1:9000"},
			want:         []string{"--sandbox-can-reach", "10.0.0.5", "--addr", "127.0.0.1:9001"},
		},
		{
			name:         "every --addr goes",
			startCommand: []string{"/usr/local/bin/deepcove", "start", "-addr", "127.0.0.1:8000", "--addr=127.0.0.1:9000"},
			want:         []string{"--addr", "127.0.0.1:9001"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildStartArgsWithAddr(tc.startCommand, "127.0.0.1:9001"); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSwitchToNextFreePortIfTaken(t *testing.T) {
	cases := []struct {
		name              string
		portTaken         bool
		exported          bool
		stored            []string
		startArgs         []string
		deepcoveHasIt     bool
		deepcoveElsewhere bool
		deepcoveTooOld    bool
		wantMoved         bool
	}{
		{name: "another app has the default port", portTaken: true, wantMoved: true},
		{name: "the port is free", portTaken: false},
		{name: "a port only this shell exports, which the service never sees", portTaken: true, exported: true, wantMoved: true},
		{name: "a port stored with --env", portTaken: true, stored: []string{"ADDR=127.0.0.1:9100"}},
		{name: "a port stored with --env, spaces around the name", portTaken: true, stored: []string{" ADDR = 127.0.0.1:9100"}},
		{name: "deepcove itself has the port", portTaken: true, deepcoveHasIt: true},
		{name: "deepcove runs, but on another port", portTaken: true, deepcoveElsewhere: true, wantMoved: true},
		{name: "an older deepcove that cannot say where it listens", portTaken: true, deepcoveTooOld: true},
		{name: "a port given with --addr", portTaken: true, startArgs: []string{"--addr", "127.0.0.1:9100"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			listenAddr := takePort(t)
			if !tc.portTaken {
				listenAddr = freedPort(t)
			}
			defaultAddr := serviceDefaultAddr
			serviceDefaultAddr = listenAddr
			t.Cleanup(func() {
				serviceDefaultAddr = defaultAddr
			})
			if tc.exported {
				t.Setenv("ADDR", "127.0.0.1:9100")
			}
			if err := config.SetEnvFileValues(dataDir, tc.stored); err != nil {
				t.Fatal(err)
			}
			addrBefore, _, err := findServiceAddr(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.deepcoveHasIt {
				serveOnSocket(t, dataDir, buildListenAddrHandler(listenAddr))
			}
			if tc.deepcoveElsewhere {
				serveOnSocket(t, dataDir, buildListenAddrHandler(takePort(t)))
			}
			if tc.deepcoveTooOld {
				serveOnSocket(t, dataDir, nil)
			}
			cfg := &config.Config{
				Addr:    listenAddr,
				DataDir: dataDir,
			}
			if err := switchToNextFreePortIfTaken(cfg, tc.startArgs); err != nil {
				t.Fatal(err)
			}
			addrAfter, _, err := findServiceAddr(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if moved := addrAfter != addrBefore; moved != tc.wantMoved {
				t.Errorf("moved = %v, want %v (%s became %s)", moved, tc.wantMoved, addrBefore, addrAfter)
			}
		})
	}
}

func TestIsSameListenAddr(t *testing.T) {
	cases := []struct {
		name       string
		servedAddr string
		listenAddr string
		want       bool
	}{
		{"the same address", "127.0.0.1:9000", "127.0.0.1:9000", true},
		{"another port", "127.0.0.1:9001", "127.0.0.1:9000", false},
		{"another address on the same port", "127.0.0.2:9000", "127.0.0.1:9000", false},
		{"served on every address", "0.0.0.0:9000", "127.0.0.1:9000", true},
		{"served on every address, written without a host", ":9000", "127.0.0.1:9000", true},
		{"asked about every address", "127.0.0.1:9000", "0.0.0.0:9000", true},
		{"the same ipv6 address written two ways", "[::1]:9000", "[0:0:0:0:0:0:0:1]:9000", true},
		{"the same name in another case", "LocalHost:9000", "localhost:9000", true},
		{"not an address", "garbage", "127.0.0.1:9000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSameListenAddr(tc.servedAddr, tc.listenAddr); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFindServiceAddr(t *testing.T) {
	cases := []struct {
		name       string
		stored     []string
		exported   string
		wantAddr   string
		wantStored bool
	}{
		{name: "nothing stored", wantAddr: config.DefaultAddr},
		{name: "a stored ADDR", stored: []string{"ADDR=127.0.0.1:9100"}, wantAddr: "127.0.0.1:9100", wantStored: true},
		{name: "only exported in this shell", exported: "127.0.0.1:9100", wantAddr: config.DefaultAddr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := config.SetEnvFileValues(dataDir, tc.stored); err != nil {
				t.Fatal(err)
			}
			if tc.exported != "" {
				t.Setenv("ADDR", tc.exported)
			}
			addr, isStored, err := findServiceAddr(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if addr != tc.wantAddr || isStored != tc.wantStored {
				t.Errorf("got %q, %v, want %q, %v", addr, isStored, tc.wantAddr, tc.wantStored)
			}
		})
	}
}

func TestBuildPortInUseErrorFrom(t *testing.T) {
	takenAddr := takePort(t)
	cases := []struct {
		name      string
		startArgs []string
		wantFix   string
	}{
		{name: "the port came from ADDR", wantFix: "deepcove config set ADDR="},
		{name: "the port came from --addr, which beats ADDR", startArgs: []string{"--addr", takenAddr}, wantFix: "with --addr "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := buildPortInUseErrorFrom(takenAddr, tc.startArgs); !strings.Contains(err.Error(), tc.wantFix) {
				t.Errorf("got %q, want it to contain %q", err, tc.wantFix)
			}
		})
	}
}

// freedPort returns the address of a port that was free a moment ago.
func freedPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}
