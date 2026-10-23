package cloudhv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/netguard"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// startVirtiofsd shares one host directory with the guest. Cloud Hypervisor has no file sharing of its
// own: it connects to this socket, and virtiofsd serves the directory behind it.
//
// virtiofsd keeps its default sandbox, so the shared directory is the only folder it can see. Even root
// in the guest, talking to virtiofsd directly, cannot then create a file anywhere else on the host.
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
//
// The networks the host joins later are blocked too: deepcove watches for them and tells the guard step,
// which stays inside that network for as long as passt runs. stopWatching ends that watch.
func startPasst(
	self, binary, socket string,
	runAs, allow []string,
	watchHost hostNetworkWatcher,
) (passt *vmproc.Process, stopWatching func(), err error) {
	hostNetworks, err := netguard.HostNetworks()
	if err != nil {
		return nil, nil, err
	}
	// A link's target is read from the link's own folder, so a relative path, from a relative DATA_DIR
	// say, would point nowhere.
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, nil, fmt.Errorf("cloudhv: find passt: %w", err)
	}
	dir := filepath.Dir(socket)
	pasta := filepath.Join(dir, "pasta")
	if err := os.Symlink(binary, pasta); err != nil {
		return nil, nil, fmt.Errorf("cloudhv: link pasta: %w", err)
	}
	control := filepath.Join(dir, "guard.sock")
	argv := pastaArgs(self, runAs, guardNetworkArgs(allow, hostNetworks, control), binary, passtArgs(socket, runAs))
	passt, err = vmproc.Start("passt", pasta, argv)
	if err != nil {
		return nil, nil, err
	}
	// The guard listens before it starts passt, so passt's socket means the guard's is there too.
	if err := passt.WaitForFile(socket, readyWait); err != nil {
		return nil, nil, errors.Join(err, passt.Stop())
	}
	// The VM starts only once the watch runs and has blocked anything joined since the list above.
	stopWatching, err = watchHost(control, hostNetworks, func() { _ = passt.Stop() })
	if err != nil {
		return nil, nil, errors.Join(err, passt.Stop())
	}
	return passt, stopWatching, nil
}

// hostNetworkWatcher starts the watch for networks the host joins and returns once it runs. A Backend
// carries one so its tests can boot a VM with no guard to talk to.
type hostNetworkWatcher func(control string, blocked []netip.Prefix, failClosed func()) (stop func(), err error)

func watchHostNetworks(control string, blocked []netip.Prefix, failClosed func()) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go keepHostNetworksBlocked(ctx, control, blocked, failClosed, netguard.WatchHostNetworks, netguard.HostNetworks, started)
	if err := <-started; err != nil {
		cancel()
		return nil, err
	}
	return cancel, nil
}

// keepHostNetworksBlocked sends every network the host joins to the VM's guard, which blocks it.
//
// started gets one answer, once the watch runs and its first look is blocked: nil, or why it could not
// start, which fails the VM's start. After that it fails closed: when a new network cannot be read or
// blocked, failClosed takes the VM's network down, since the VM could otherwise reach that network.
//
// It also holds a connection to the guard open for as long as it watches. If deepcove goes away, the
// connection closes and the guard stops passt, so a VM never outlives the watch with its network up.
func keepHostNetworksBlocked(
	ctx context.Context,
	control string,
	blocked []netip.Prefix,
	failClosed func(),
	watch func(context.Context, func()) error,
	read func() ([]netip.Prefix, error),
	started chan<- error,
) {
	// running is set once the start is answered; a failure before that is the start's answer, and only
	// the first one is sent.
	var running, answered bool
	fail := func(err error) {
		if !running {
			if !answered {
				answered = true
				started <- err
			}
			return
		}
		slog.Error("cloudhv: taking a VM's network down, a network the host joined could not be blocked", "err", err)
		failClosed()
	}
	hold, err := holdGuard(control)
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = hold.Close() }()
	sent := make(map[netip.Prefix]bool, len(blocked))
	for _, network := range blocked {
		sent[network] = true
	}
	blockFresh := func() error {
		networks, err := read()
		if err != nil {
			return err
		}
		fresh := unsentNetworks(networks, sent)
		if len(fresh) == 0 {
			return nil
		}
		if err := sendBlockRequest(control, fresh); err != nil {
			return err
		}
		for _, network := range fresh {
			sent[network] = true
		}
		return nil
	}
	err = watch(ctx, func() {
		if answered && !running {
			return
		}
		err := blockFresh()
		if err != nil {
			fail(err)
			return
		}
		if !running {
			running, answered = true, true
			started <- nil
		}
	})
	if err != nil && ctx.Err() == nil {
		fail(err)
	}
}

// holdGuard tells the VM's guard that deepcove is watching, on a connection that stays open until it
// stops.
func holdGuard(control string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", control, guardReplyWait)
	if err != nil {
		return nil, fmt.Errorf("cloudhv: reach the network guard: %w", err)
	}
	if _, err := fmt.Fprintln(conn, "hold"); err != nil {
		return nil, errors.Join(fmt.Errorf("cloudhv: reach the network guard: %w", err), conn.Close())
	}
	return conn, nil
}

func unsentNetworks(networks []netip.Prefix, sent map[netip.Prefix]bool) []netip.Prefix {
	var fresh []netip.Prefix
	for _, network := range networks {
		if !sent[network] {
			fresh = append(fresh, network)
		}
	}
	return fresh
}

// sendBlockRequest asks the VM's guard to block networks, one per line, and waits for its answer.
func sendBlockRequest(control string, networks []netip.Prefix) error {
	conn, err := net.DialTimeout("unix", control, guardReplyWait)
	if err != nil {
		return fmt.Errorf("cloudhv: reach the network guard: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(guardReplyWait))
	for _, network := range networks {
		if _, err := fmt.Fprintln(conn, network); err != nil {
			return fmt.Errorf("cloudhv: send to the network guard: %w", err)
		}
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return fmt.Errorf("cloudhv: send to the network guard: %w", err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("cloudhv: hear back from the network guard: %w", err)
	}
	if reply = strings.TrimSpace(reply); reply != "ok" {
		return fmt.Errorf("cloudhv: the network guard said %s", reply)
	}
	return nil
}

// guardReplyWait bounds one block request. Adding a few routes takes milliseconds.
const guardReplyWait = 5 * time.Second

// pastaArgs makes the private network and runs deepcove in it, which blocks networks there and then
// runs passt.
//
// Without the flags below, pasta would carry the guest back to the host it is kept off: it maps its
// gateway to the host's own 127.0.0.1 (deepcove's API listens there), forwards ports bound on the host's
// loopback, and copies the host's addresses, whose LAN route would be more specific than the block.
//
// runAs is passt's, which pasta needs too: it is the same program, and as root without the AppArmor
// profile it would drop to nobody before making its network and fail.
func pastaArgs(self string, runAs, guard []string, passt string, passtArgv []string) []string {
	args := append([]string{"--config-net", "--quiet", "--no-map-gw",
		"-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"-a", pastaAddress, "-n", pastaPrefix, "-g", pastaGateway,
		// The host's DNS server is often on a private address the block drops: a cloud resolver, a home
		// router, Tailscale. pasta answers this address from the host side, with the host's resolver.
		"--dns-forward", pastaDNS}, runAs...)
	args = append(args, "--", self, "vm", "guard-network")
	args = append(args, guard...)
	args = append(args, "--", passt)
	return append(args, passtArgv...)
}

// guardNetworkArgs tells deepcove what to block besides the private ranges: the host's own networks,
// which can be outside them (a LAN's IPv6 prefix), and what the operator allowed. control is the socket
// it hears about networks the host joins later on.
func guardNetworkArgs(allow []string, hostNetworks []netip.Prefix, control string) []string {
	args := []string{"--control", control}
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
