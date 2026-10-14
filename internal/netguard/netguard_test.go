package netguard

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseAllowList(t *testing.T) {
	cases := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{name: "nothing allowed", input: nil, want: []string{}},
		{
			name:  "a bare address becomes a host route",
			input: []string{"100.95.206.36"},
			want:  []string{"100.95.206.36/32"},
		},
		{name: "a CIDR is kept", input: []string{"10.1.0.0/16"}, want: []string{"10.1.0.0/16"}},
		{
			name:  "a CIDR with host bits set is masked",
			input: []string{"10.1.2.3/16"},
			want:  []string{"10.1.0.0/16"},
		},
		{name: "IPv6 works too", input: []string{"fd7a:115c:a1e0::1"}, want: []string{"fd7a:115c:a1e0::1/128"}},
		{name: "blank entries are skipped", input: []string{"", "  "}, want: []string{}},
		{name: "not an address", input: []string{"not-an-ip"}, wantErr: true},
		{name: "a hostname is not accepted", input: []string{"example.com"}, wantErr: true},
		{name: "a bad CIDR", input: []string{"10.0.0.0/99"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAllowList(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAllowList: %v", err)
			}
			var out []string
			for _, prefix := range got {
				out = append(out, prefix.String())
			}
			if strings.Join(out, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", out, tc.want)
			}
		})
	}
}

// The allowed address only takes effect if it is a LONGER prefix than the block covering it, since the
// kernel picks the longest match. This pins that relationship rather than the routes themselves.
func TestAllowedPrefixesBeatTheirBlock(t *testing.T) {
	allowed, err := ParseAllowList([]string{"100.95.206.36", "192.168.1.0/24", "fd7a:115c:a1e0::1"})
	if err != nil {
		t.Fatalf("ParseAllowList: %v", err)
	}
	for _, allowedPrefix := range allowed {
		var covered bool
		for _, blocked := range blockedPrefixes {
			if blocked.Overlaps(allowedPrefix) {
				covered = true
				if allowedPrefix.Bits() <= blocked.Bits() {
					t.Errorf("%s is not more specific than the block %s, so it would be ignored", allowedPrefix, blocked)
				}
			}
		}
		if !covered {
			t.Errorf("%s is not inside any blocked prefix, so allowing it does nothing", allowedPrefix)
		}
	}
}

func TestBlockedPrefixesCoverTheTailnet(t *testing.T) {
	// A tailnet peer's addresses, both families. These are what the sandbox could reach before.
	for _, addr := range []string{"100.95.206.36", "fd7a:115c:a1e0::1"} {
		peer := netip.MustParseAddr(addr)
		var blocked bool
		for _, prefix := range blockedPrefixes {
			if prefix.Contains(peer) {
				blocked = true
			}
		}
		if !blocked {
			t.Errorf("%s is reachable: no blocked prefix covers it", addr)
		}
	}
}

func TestRefuses(t *testing.T) {
	allowed, err := ParseAllowList([]string{"100.95.206.36", "127.0.0.1", "10.0.0.0/7", "172.16.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	hostNetworks := []netip.Prefix{netip.MustParsePrefix("2001:db8:5::/64")}
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"the internet", "93.184.215.14", false},
		{"a tailnet peer", "100.121.26.50", true},
		{"the allowed tailnet peer", "100.95.206.36", false},
		{"cloud metadata", "169.254.169.254", true},
		{"a home router", "192.168.1.1", true},
		{"IPv4 written as IPv6", "::ffff:10.0.0.1", true},
		{"a tailnet IPv6 peer", "fd7a:115c:a1e0::1", true},
		{"the IPv6 internet", "2606:4700::1", false},
		{"the host's own loopback, even when allowed", "127.0.0.1", true},
		{"the host's IPv6 loopback", "::1", true},
		{"a machine on the host's IPv6 LAN", "2001:db8:5::10", true},
		{"an allowed network wider than its block stays blocked", "10.1.2.3", true},
		{"an allowed network as wide as its block gets through", "172.16.5.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Refuses(netip.MustParseAddr(tc.addr), allowed, hostNetworks); got != tc.want {
				t.Errorf("Refuses(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestHostNetworks_LeavesOutLoopback(t *testing.T) {
	networks, err := HostNetworks()
	if err != nil {
		t.Fatalf("HostNetworks: %v", err)
	}
	for _, network := range networks {
		if network.Addr().IsLoopback() || network.Addr().IsLinkLocalUnicast() {
			t.Errorf("%s is listed, but a route for it would break the network it is installed in", network)
		}
	}
}
