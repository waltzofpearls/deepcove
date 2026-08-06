package cli

import (
	"slices"
	"testing"
)

func TestLocalAllowedHosts(t *testing.T) {
	cases := []struct {
		addr string
		want []string
	}{
		{"127.0.0.1:8080", []string{"127.0.0.1"}},
		{"0.0.0.0:8080", nil}, // wildcard contributes nothing; only loopback trusted
		{"[::]:8080", nil},    // ipv6 wildcard
		{"deepcove.lan:8080", []string{"deepcove.lan"}},
		{"127.0.0.1", []string{"127.0.0.1"}}, // no port
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := localAllowedHosts(tc.addr); !slices.Equal(got, tc.want) {
				t.Errorf("localAllowedHosts(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestSocketPath(t *testing.T) {
	a := socketPath("/tmp/dc-a")
	b := socketPath("/tmp/dc-b")
	if a == "" || b == "" {
		t.Fatal("socketPath returned empty")
	}
	// Distinct data dirs must give distinct sockets, so multiple instances on one machine don't collide.
	if a == b {
		t.Errorf("distinct data dirs share a socket path: %q", a)
	}
	// The same data dir must give the same socket, so the server and CLI agree without extra config.
	if got := socketPath("/tmp/dc-a"); got != a {
		t.Errorf("same data dir gave different sockets: %q vs %q", a, got)
	}
}
