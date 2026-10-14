// Package netguard keeps a sandbox off the networks around it. The same list is enforced twice: inside
// the guest with routes, and on the host, where the guest cannot change it.
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// A sandbox needs the internet. It does not need the machines around it: the host's LAN, its tailnet
// peers, or a cloud metadata service. Those are reachable today only because the VM's NAT inherits the
// host's routes, which makes the sandbox the best connected thing on the network rather than the least.
//
// The block is routing, not filtering. A blackhole route makes the kernel drop the packet itself, so
// this needs no netfilter and no iptables or nft binary in the image, neither of which we can count on.

// blockedPrefixes are the networks a sandbox must not reach. Both families, for a host that gives the
// guest IPv6.
//
// The guest's own subnet is deliberately absent from the guest's routes, so the gateway and the internet
// keep working (its route is more specific). Nothing else is on it: the host gives every VM a network
// of its own.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier grade NAT, where tailnet addresses live
	netip.MustParsePrefix("169.254.0.0/16"), // link local, which covers cloud metadata
	netip.MustParsePrefix("fc00::/7"),       // unique local, where Tailscale's fd7a: addresses live
	netip.MustParsePrefix("fe80::/10"),      // link local
}

// ParseAllowList accepts a bare address or a CIDR, and turns a bare address into a host route.
func ParseAllowList(allow []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(allow))
	for _, raw := range allow {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("allowed host %q: %w", raw, err)
			}
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("allowed host %q: %w", raw, err)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// hostOnlyPrefixes never leave the guest on their own. A guest that sends to them anyway would reach the
// host itself, its own deepcove included, so the host refuses them even when allowed.
var hostOnlyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("224.0.0.0/3"), // multicast and reserved
	netip.MustParsePrefix("::/127"),      // unspecified and loopback
	netip.MustParsePrefix("ff00::/8"),
}

// Refuses reports whether the host refuses to carry a sandbox's traffic to addr. hostNetworks are the
// networks the host itself sits on, which can be outside the private ranges, a LAN's IPv6 prefix for one.
//
// The most specific match decides, as with the routes on Linux, so an allowed address only gets through
// where it is at least as specific as the block it sits in. Both backends then agree on every setting.
func Refuses(addr netip.Addr, allowed, hostNetworks []netip.Prefix) bool {
	addr = addr.Unmap()
	if longestMatch(hostOnlyPrefixes, addr) >= 0 {
		return true
	}
	blockedBits := max(longestMatch(blockedPrefixes, addr), longestMatch(hostNetworks, addr))
	return blockedBits >= 0 && longestMatch(allowed, addr) < blockedBits
}

// HostNetworks are the networks this machine's interfaces sit on, its LAN included. Loopback is left out:
// Refuses covers it, and a route for it would break the loopback of the network it is installed in.
//
// An error means the list may be short, so the caller must not start a VM's network with it.
func HostNetworks() ([]netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("netguard: list this machine's networks: %w", err)
	}
	var networks []netip.Prefix
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("netguard: list the addresses of %s: %w", iface.Name, err)
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok || ip.IsLinkLocalUnicast() {
				continue
			}
			ones, _ := ipNet.Mask.Size()
			networks = append(networks, netip.PrefixFrom(ip.Unmap(), ones).Masked())
		}
	}
	return networks, nil
}

// longestMatch is the length of the most specific prefix holding addr, or -1 when none does.
func longestMatch(prefixes []netip.Prefix, addr netip.Addr) int {
	longest := -1
	for _, prefix := range prefixes {
		if prefix.Contains(addr) && prefix.Bits() > longest {
			longest = prefix.Bits()
		}
	}
	return longest
}
