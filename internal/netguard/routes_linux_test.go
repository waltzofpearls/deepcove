//go:build linux

package netguard

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseGateway4(t *testing.T) {
	// Real /proc/net/route output. Addresses are hex in host byte order.
	const proc = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	0042A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
eth0	00000000	0142A8C0	0003	0	0	202	00000000	0	0	0
`
	got, err := parseGateway4(strings.NewReader(proc))
	if err != nil {
		t.Fatalf("parseGateway4: %v", err)
	}
	if got.String() != "192.168.66.1" {
		t.Errorf("gateway: got %s, want 192.168.66.1", got)
	}
}

func TestParseGateway6(t *testing.T) {
	// Real /proc/net/ipv6_route output; the default route is the all zero destination with plen 00.
	const proc = `fd8e070cf2c384aa0000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 004c0001     eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe80000000000000a4fc14fffe10d164 00000400 00000001 00000000 00450003     eth0
`
	got, err := parseGateway6(strings.NewReader(proc))
	if err != nil {
		t.Fatalf("parseGateway6: %v", err)
	}
	if got.String() != "fe80::a4fc:14ff:fe10:d164" {
		t.Errorf("gateway: got %s, want fe80::a4fc:14ff:fe10:d164", got)
	}
}

func TestNoDefaultRouteIsAnError(t *testing.T) {
	if _, err := parseGateway4(strings.NewReader("Iface\tDestination\n")); err == nil {
		t.Error("expected an error when there is no default route")
	}
	if _, err := parseGateway6(strings.NewReader("")); err == nil {
		t.Error("expected an error when there is no default route")
	}
}

func TestEncodeRoute(t *testing.T) {
	cases := []struct {
		name       string
		dst        string
		kind       uint8
		gateway    string
		wantFamily uint8
		wantBits   uint8
		wantGw     bool
	}{
		{
			name: "an IPv4 blackhole", dst: "10.0.0.0/8", kind: unix.RTN_BLACKHOLE,
			wantFamily: unix.AF_INET, wantBits: 8,
		},
		{
			name: "an IPv6 blackhole", dst: "fc00::/7", kind: unix.RTN_BLACKHOLE,
			wantFamily: unix.AF_INET6, wantBits: 7,
		},
		{
			name: "an allowed host via the gateway", dst: "100.95.206.36/32", kind: unix.RTN_UNICAST,
			gateway: "192.168.66.1", wantFamily: unix.AF_INET, wantBits: 32, wantGw: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gw netip.Addr
			if tc.gateway != "" {
				gw = netip.MustParseAddr(tc.gateway)
			}
			msg := encodeRoute(netip.MustParsePrefix(tc.dst), tc.kind, gw, 7)

			if got := int(msg[0]) | int(msg[1])<<8; got != len(msg) {
				t.Errorf("header length %d does not match the message length %d", got, len(msg))
			}
			if len(msg)%4 != 0 {
				t.Errorf("message length %d is not 4 byte aligned", len(msg))
			}
			body := msg[unix.NLMSG_HDRLEN:]
			if body[0] != tc.wantFamily {
				t.Errorf("family: got %d, want %d", body[0], tc.wantFamily)
			}
			if body[1] != tc.wantBits {
				t.Errorf("prefix length: got %d, want %d", body[1], tc.wantBits)
			}
			if body[7] != tc.kind {
				t.Errorf("route type: got %d, want %d", body[7], tc.kind)
			}
			// A gateway attribute is what makes an allowed route reachable rather than on link.
			hasGw := containsAttr(body[12:], unix.RTA_GATEWAY)
			if hasGw != tc.wantGw {
				t.Errorf("gateway attribute present = %v, want %v", hasGw, tc.wantGw)
			}
			if !containsAttr(body[12:], unix.RTA_DST) {
				t.Error("destination attribute missing")
			}
		})
	}
}

// containsAttr walks the netlink attributes looking for one of the given type.
func containsAttr(attrs []byte, kind uint16) bool {
	for len(attrs) >= unix.SizeofRtAttr {
		length := int(attrs[0]) | int(attrs[1])<<8
		got := uint16(attrs[2]) | uint16(attrs[3])<<8
		if got == kind {
			return true
		}
		if length < unix.SizeofRtAttr || length > len(attrs) {
			return false
		}
		padded := (length + 3) &^ 3
		if padded > len(attrs) {
			return false
		}
		attrs = attrs[padded:]
	}
	return false
}

// An address the operator allowed but the guest cannot install must not cost the session its network:
// the block is already in place, so the safe thing has happened and only that address is missing.
func TestOpenAllowedReportsRatherThanGivesUp(t *testing.T) {
	allowed := []netip.Prefix{
		netip.MustParsePrefix("100.75.35.244/32"),
		netip.MustParsePrefix("fd7a:115c:a1e0::1/128"),
		netip.MustParsePrefix("192.168.1.10/32"),
	}
	// No IPv6 default route, which is the real case: vmnet gives the guest no path to the IPv6 internet.
	gatewayFor := func(ipv4 bool) (netip.Addr, error) {
		if !ipv4 {
			return netip.Addr{}, errors.New("no default route")
		}
		return netip.MustParseAddr("192.168.64.1"), nil
	}
	var installed []netip.Prefix
	addRoute := func(prefix netip.Prefix, _ netip.Addr) error {
		installed = append(installed, prefix)
		return nil
	}

	warnings := openAllowed(allowed, gatewayFor, addRoute)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "fd7a") {
		t.Fatalf("warnings = %v, want one about the IPv6 address", warnings)
	}
	// The other two still went in: one failure does not stop the rest.
	if len(installed) != 2 {
		t.Errorf("installed %v, want both IPv4 addresses", installed)
	}
}
