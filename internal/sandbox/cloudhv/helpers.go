package cloudhv

import (
	"errors"
	"net/netip"
	"os"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// startVirtiofsd shares one host directory with the guest. Cloud Hypervisor has no file sharing of its
// own: it connects to this socket, and virtiofsd serves the directory behind it.
//
// `--sandbox none` because the namespace sandbox needs privileges we do not want to require, and the
// isolation that matters is the VM's, not the daemon's.
func startVirtiofsd(binary, socket string, share vm.Share) (*child, error) {
	argv := []string{
		"--socket-path", socket,
		"--shared-dir", share.HostPath,
		"--tag", share.Tag,
		"--sandbox", "none",
		"--cache", "auto",
	}
	if share.ReadOnly {
		argv = append(argv, "--readonly")
	}
	c, err := startChild("virtiofsd for "+share.Tag, binary, argv)
	if err != nil {
		return nil, err
	}
	if err := waitForFile(c, socket, readyWait); err != nil {
		return nil, errors.Join(err, c.Stop())
	}
	return c, nil
}

// startPasst gives the guest a network with no privileges on the host. passt creates the socket and the
// VMM connects to it, which is why the VMM is the client.
func startPasst(binary, socket string, runAs, nameservers []string) (*child, error) {
	c, err := startChild("passt", binary, passtArgs(socket, runAs, nameservers))
	if err != nil {
		return nil, err
	}
	if err := waitForFile(c, socket, readyWait); err != nil {
		return nil, errors.Join(err, c.Stop())
	}
	return c, nil
}

// passtArgs keeps the guest off the host. passt's defaults would map the gateway to the host's 127.0.0.1
// (deepcove's own API) and give the guest the host's address (putting the host's network on the guest's
// subnet). So the guest gets a made-up address on a reserved network instead.
//
// DNS: the guest is always told to use the two addresses below, and passt sends what arrives there to the
// host's own DNS servers. Handing the guest the host's servers directly fails when they are private
// (Tailscale, a cloud resolver, a home router), since the guest's guard drops private addresses.
func passtArgs(socket string, runAs, nameservers []string) []string {
	args := []string{"--vhost-user", "--socket", socket, "--foreground", "--quiet", "--no-map-gw",
		"-a", passtGuestAddress, "-n", passtGuestPrefix, "-g", passtGuestGateway,
		"-D", passtDNSv4, "-D", passtDNSv6, "--dns-forward", passtDNSv4, "--dns-forward", passtDNSv6}
	for _, server := range nameservers {
		args = append(args, "--dns-host", server)
	}
	return append(args, runAs...)
}

// hostNameservers returns the host's first IPv4 and first IPv6 DNS server from resolv.conf. passt reads
// that file itself only when the guest's DNS is left to it, which passtArgs no longer does.
func hostNameservers(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v4, v6 string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		switch {
		// passt refuses these and then will not start, so no VM could boot.
		case err != nil, addr.Zone() != "", addr.IsLinkLocalUnicast(), addr.IsUnspecified():
		case addr.Is4() && v4 == "":
			v4 = addr.String()
		case addr.Is6() && v6 == "":
			v6 = addr.String()
		}
	}
	var servers []string
	for _, server := range []string{v4, v6} {
		if server != "" {
			servers = append(servers, server)
		}
	}
	return servers
}

// The guest's network. TEST-NET-2 is reserved, so no real machine is on it.
const (
	passtGuestAddress = "198.51.100.2"
	passtGuestPrefix  = "24"
	passtGuestGateway = "198.51.100.1"
)

// Where the guest sends DNS. Both are reserved documentation addresses, so never a real host, and the
// guest's network guard does not drop them.
const (
	passtDNSv4 = "192.0.2.1"
	passtDNSv6 = "2001:db8::1"
)

// stopAll stops helpers in reverse order and reports everything that went wrong.
func stopAll(helpers []*child) error {
	var err error
	for i := len(helpers) - 1; i >= 0; i-- {
		err = errors.Join(err, helpers[i].Stop())
	}
	return err
}
