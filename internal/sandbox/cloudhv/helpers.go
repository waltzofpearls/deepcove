package cloudhv

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/waltzofpearls/deepcove/internal/netguard"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// startVirtiofsd shares one host directory with the guest. Cloud Hypervisor has no file sharing of its
// own: it connects to this socket, and virtiofsd serves the directory behind it.
//
// `--sandbox none` because the namespace sandbox needs privileges we do not want to require, and the
// isolation that matters is the VM's, not the daemon's.
func startVirtiofsd(binary, socket string, share vm.Share) (*vmproc.Process, error) {
	sharedDir, workDir := share.HostPath, ""
	if share.Folder != nil {
		// Started inside the folder deepcove opened and checked, so a link swapped into the path since
		// then changes nothing.
		sharedDir, workDir = ".", fmt.Sprintf("/proc/self/fd/%d", share.Folder.Fd())
	}
	argv := []string{
		"--socket-path", socket,
		"--shared-dir", sharedDir,
		"--tag", share.Tag,
		"--sandbox", "none",
		"--cache", "auto",
	}
	if share.ReadOnly {
		argv = append(argv, "--readonly")
	}
	virtiofsd, err := vmproc.StartIn("virtiofsd for "+share.Tag, binary, workDir, argv)
	if err != nil {
		return nil, err
	}
	if err := virtiofsd.WaitForFile(socket, readyWait); err != nil {
		return nil, errors.Join(err, virtiofsd.Stop())
	}
	return virtiofsd, nil
}

// startPasst gives the guest a network with no privileges on the host. passt creates the socket and the
// VMM connects to it, which is why the VMM is the client.
//
// passt runs inside a private network that pasta makes, where deepcove has blocked the networks a sandbox
// must not reach. Every connection the guest opens is made from there, so root in the guest cannot get
// around the block, as it can with the routes inside the guest. pasta is passt's own binary under its
// other name, so it is linked rather than shipped.
func startPasst(self, binary, socket string, runAs, allow []string) (*vmproc.Process, error) {
	hostNetworks, err := netguard.HostNetworks()
	if err != nil {
		return nil, err
	}
	pasta := filepath.Join(filepath.Dir(socket), "pasta")
	if err := os.Symlink(binary, pasta); err != nil {
		return nil, fmt.Errorf("cloudhv: link pasta: %w", err)
	}
	argv := pastaArgs(self, guardNetworkArgs(allow, hostNetworks), binary, passtArgs(socket, runAs))
	passt, err := vmproc.Start("passt", pasta, argv)
	if err != nil {
		return nil, err
	}
	if err := passt.WaitForFile(socket, readyWait); err != nil {
		return nil, errors.Join(err, passt.Stop())
	}
	return passt, nil
}

// pastaArgs makes the private network and runs deepcove in it, which blocks networks there and then
// becomes passt.
//
// Without the flags below, pasta would carry the guest back to the host it is kept off: it maps its
// gateway to the host's own 127.0.0.1 (deepcove's API listens there), forwards ports bound on the host's
// loopback, and copies the host's addresses, whose LAN route would be more specific than the block.
func pastaArgs(self string, guard []string, passt string, passtArgv []string) []string {
	args := []string{"--config-net", "--quiet", "--no-map-gw",
		"-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"-a", pastaAddress, "-n", pastaPrefix, "-g", pastaGateway,
		// The host's DNS server is often on a private address the block drops: a cloud resolver, a home
		// router, Tailscale. pasta answers this address from the host side, with the host's resolver.
		"--dns-forward", pastaDNS,
		"--", self, "vm", "guard-network"}
	args = append(args, guard...)
	args = append(args, "--", passt)
	return append(args, passtArgv...)
}

// guardNetworkArgs tells deepcove what to block besides the private ranges: the host's own networks,
// which can be outside them (a LAN's IPv6 prefix), and what the operator allowed.
func guardNetworkArgs(allow []string, hostNetworks []netip.Prefix) []string {
	var args []string
	for _, prefix := range hostNetworks {
		args = append(args, "--block", prefix.String())
	}
	for _, addr := range allow {
		args = append(args, "--allow", addr)
	}
	return args
}

// passtArgs keeps the guest off the host. passt's defaults would map the gateway to the host's 127.0.0.1
// (deepcove's own API) and give the guest the host's address (putting the host's network on the guest's
// subnet). So the guest gets a made-up address on a reserved network instead.
//
// DNS: the guest is always told to use the two addresses below, and passt sends what arrives there to
// pasta, which asks the host's resolver.
func passtArgs(socket string, runAs []string) []string {
	args := []string{"--vhost-user", "--socket", socket, "--foreground", "--quiet", "--no-map-gw",
		"-a", passtGuestAddress, "-n", passtGuestPrefix, "-g", passtGuestGateway,
		"-D", passtDNSv4, "-D", passtDNSv6, "--dns-forward", passtDNSv4, "--dns-forward", passtDNSv6,
		"--dns-host", pastaDNS}
	return append(args, runAs...)
}

// The private network passt runs in. 198.18.0.0/15 is reserved for benchmarks, so no real machine is on
// it, and it is not one of the blocked ranges.
const (
	pastaAddress = "198.18.0.2"
	pastaPrefix  = "24"
	pastaGateway = "198.18.0.1"
	// pastaDNS is where passt asks for DNS inside that network, and pasta answers.
	pastaDNS = "192.0.2.53"
)

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
func stopAll(helpers []*vmproc.Process) error {
	var err error
	for i := len(helpers) - 1; i >= 0; i-- {
		err = errors.Join(err, helpers[i].Stop())
	}
	return err
}
