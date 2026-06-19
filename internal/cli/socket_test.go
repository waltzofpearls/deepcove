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
		{"0.0.0.0:8080", nil},  // wildcard contributes nothing; only loopback trusted
		{"[::]:8080", nil},     // ipv6 wildcard
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

func TestSocketPathNonEmpty(t *testing.T) {
	if p := socketPath(); p == "" {
		t.Fatal("socketPath() returned empty")
	}
}
