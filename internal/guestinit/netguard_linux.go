//go:build linux

package guestinit

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// A sandbox needs the internet. It does not need the machines around it: the host's LAN, its tailnet
// peers, or a cloud metadata service. Those are reachable today only because the VM's NAT inherits the
// host's routes, which makes the sandbox the best connected thing on the network rather than the least.
//
// The block is routing, not filtering. A blackhole route makes the kernel drop the packet itself, so
// this needs no netfilter and no iptables or nft binary in the image, neither of which we can count on.

// blockedPrefixes are the networks a sandbox must not reach. Both families: the guest gets an IPv6
// address and a default route even though vmnet gives it no path to the IPv6 internet.
//
// The guest's own subnet is deliberately absent. DHCP and router advertisement install a more specific
// route for it, and the kernel picks the longest match, so the gateway and the internet keep working.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier grade NAT, where tailnet addresses live
	netip.MustParsePrefix("169.254.0.0/16"), // link local, which covers cloud metadata
	netip.MustParsePrefix("fc00::/7"),       // unique local, where Tailscale's fd7a: addresses live
	netip.MustParsePrefix("fe80::/10"),      // link local
}

// applyNetGuard blackholes those prefixes, then reopens any address the operator allowed. Allowed
// addresses are installed as a longer prefix so they win over the blackhole covering them.
//
// It runs after DHCP, because an allowed address needs the gateway, and before the entrypoint, so no
// agent code ever runs while the sandbox can still reach the network around it.
//
// The two halves fail differently. Failing to install the block is fatal, because the sandbox would be
// left reachable. Failing to open a hole is not: the block is already in place, so the sandbox is safe
// and only that one address is missing. Those come back as warnings.
func applyNetGuard(allow []string) (warnings []string, err error) {
	allowed, err := parseAllowList(allow)
	if err != nil {
		return nil, err
	}

	nl, err := dialNetlink()
	if err != nil {
		return nil, fmt.Errorf("open netlink: %w", err)
	}
	defer func() { _ = nl.Close() }()

	for _, prefix := range blockedPrefixes {
		if err := nl.addRoute(prefix, unix.RTN_BLACKHOLE, netip.Addr{}); err != nil {
			return nil, fmt.Errorf("blackhole %s: %w", prefix, err)
		}
	}

	return openAllowed(allowed, defaultGateway, func(prefix netip.Prefix, gateway netip.Addr) error {
		return nl.addRoute(prefix, unix.RTN_UNICAST, gateway)
	}), nil
}

// openAllowed installs a route for each allowed address and reports the ones it could not, rather than
// giving up on the rest. Its two dependencies are arguments so the reporting can be tested without a
// netlink socket.
func openAllowed(
	allowed []netip.Prefix,
	gatewayFor func(ipv4 bool) (netip.Addr, error),
	addRoute func(netip.Prefix, netip.Addr) error,
) []string {
	var warnings []string
	for _, prefix := range allowed {
		gateway, err := gatewayFor(prefix.Addr().Is4())
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s stays blocked: %v", prefix, err))
			continue
		}
		if err := addRoute(prefix, gateway); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s stays blocked: %v", prefix, err))
		}
	}
	return warnings
}

// parseAllowList accepts a bare address or a CIDR, and turns a bare address into a host route.
func parseAllowList(allow []string) ([]netip.Prefix, error) {
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

// allowLabel describes the holes in the block for the boot log, so a session never opens one silently.
func allowLabel(allow []string) string {
	if len(allow) == 0 {
		return ""
	}
	return ", except " + strings.Join(allow, ", ")
}

// defaultGateway reads the next hop of the default route from /proc, which is where the DHCP client
// left it. Reading the kernel's answer beats assuming the gateway is the first address in the subnet.
func defaultGateway(ipv4 bool) (netip.Addr, error) {
	path, parse := "/proc/net/ipv6_route", parseGateway6
	if ipv4 {
		path, parse = "/proc/net/route", parseGateway4
	}
	f, err := os.Open(path)
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}

// parseGateway4 pulls the next hop out of /proc/net/route. Addresses there are hex in host byte order,
// which on every machine we run is little endian.
func parseGateway4(r io.Reader) (netip.Addr, error) {
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		raw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(raw))
		return netip.AddrFrom4(b), nil
	}
	if err := scan.Err(); err != nil {
		return netip.Addr{}, err
	}
	return netip.Addr{}, fmt.Errorf("no default route")
}

// parseGateway6 pulls the next hop out of /proc/net/ipv6_route, whose default route is the entry with
// an all zero destination and a zero prefix length.
func parseGateway6(r io.Reader) (netip.Addr, error) {
	const zeroAddr = "00000000000000000000000000000000"
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) < 5 || fields[0] != zeroAddr || fields[1] != "00" {
			continue
		}
		var b [16]byte
		if _, err := hex.Decode(b[:], []byte(fields[4])); err != nil {
			continue
		}
		return netip.AddrFrom16(b), nil
	}
	if err := scan.Err(); err != nil {
		return netip.Addr{}, err
	}
	return netip.Addr{}, fmt.Errorf("no default route")
}

// netlinkConn is one rtnetlink socket. Routes are added by hand rather than by shelling out to `ip`,
// which the image may not ship, and which would make a security control depend on the user's image.
type netlinkConn struct {
	fd  int
	seq uint32
}

func dialNetlink() (*netlinkConn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &netlinkConn{fd: fd}, nil
}

func (c *netlinkConn) Close() error { return unix.Close(c.fd) }

// addRoute installs one route, replacing any existing route for the same prefix so a re-run is
// harmless and an allowed address can override the blackhole that covers it.
func (c *netlinkConn) addRoute(dst netip.Prefix, kind uint8, gateway netip.Addr) error {
	c.seq++
	msg := encodeRoute(dst, kind, gateway, c.seq)
	if err := unix.Sendto(c.fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	return c.readAck()
}

// readAck waits for the kernel's verdict. Netlink reports success as an error message carrying zero.
func (c *netlinkConn) readAck() error {
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(c.fd, buf, 0)
	if err != nil {
		return err
	}
	if n < unix.NLMSG_HDRLEN {
		return fmt.Errorf("short netlink reply (%d bytes)", n)
	}
	msgType := binary.LittleEndian.Uint16(buf[4:6])
	if msgType != unix.NLMSG_ERROR {
		return nil
	}
	if n < unix.NLMSG_HDRLEN+4 {
		return fmt.Errorf("truncated netlink error")
	}
	code := int32(binary.LittleEndian.Uint32(buf[unix.NLMSG_HDRLEN : unix.NLMSG_HDRLEN+4]))
	if code == 0 {
		return nil
	}
	return unix.Errno(-code)
}

// encodeRoute builds an RTM_NEWROUTE message. Kept separate from the socket so it can be tested.
func encodeRoute(dst netip.Prefix, kind uint8, gateway netip.Addr, seq uint32) []byte {
	family := uint8(unix.AF_INET)
	if dst.Addr().Is6() {
		family = unix.AF_INET6
	}

	body := make([]byte, 12)
	body[0] = family
	body[1] = uint8(dst.Bits())
	body[4] = unix.RT_TABLE_MAIN
	body[5] = unix.RTPROT_STATIC
	body[6] = unix.RT_SCOPE_UNIVERSE
	body[7] = kind
	body = appendAttr(body, unix.RTA_DST, dst.Addr().AsSlice())
	if gateway.IsValid() {
		body = appendAttr(body, unix.RTA_GATEWAY, gateway.AsSlice())
	}

	msg := make([]byte, unix.NLMSG_HDRLEN+len(body))
	binary.LittleEndian.PutUint32(msg[0:4], uint32(len(msg)))
	binary.LittleEndian.PutUint16(msg[4:6], unix.RTM_NEWROUTE)
	binary.LittleEndian.PutUint16(msg[6:8],
		unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_REPLACE)
	binary.LittleEndian.PutUint32(msg[8:12], seq)
	copy(msg[unix.NLMSG_HDRLEN:], body)
	return msg
}

// appendAttr adds one netlink attribute, padded to the 4 byte alignment netlink requires.
func appendAttr(dst []byte, kind uint16, value []byte) []byte {
	length := unix.SizeofRtAttr + len(value)
	header := make([]byte, unix.SizeofRtAttr)
	binary.LittleEndian.PutUint16(header[0:2], uint16(length))
	binary.LittleEndian.PutUint16(header[2:4], kind)
	dst = append(dst, header...)
	dst = append(dst, value...)
	for len(dst)%4 != 0 {
		dst = append(dst, 0)
	}
	return dst
}
