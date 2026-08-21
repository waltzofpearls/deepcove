//go:build linux

package guestinit

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/unix"
)

// setupNetwork brings the loopback and primary interfaces up and asks for a DHCP lease. vmnet NAT runs a
// DHCP server on the host side, which is how the guest gets an address, a route, and DNS.
//
// A failure here is reported but never fatal: a session without network is degraded, not dead, and the
// caller keeps booting so the console and exec still work for debugging.
func setupNetwork() error {
	if err := linkUp(loopbackInterface); err != nil {
		return fmt.Errorf("bring up %s: %w", loopbackInterface, err)
	}
	if err := linkUp(primaryInterface); err != nil {
		return fmt.Errorf("bring up %s: %w", primaryInterface, err)
	}
	return runDHCP()
}

// linkUp sets IFF_UP on an interface using ioctls directly, so it works without an `ip` or `ifconfig`
// binary in the image.
func linkUp(name string) error { return setLinkFlag(name, true) }

// linkDown clears IFF_UP. Used when the sandbox network cannot be restricted, where no network is the
// safer answer.
func linkDown(name string) error { return setLinkFlag(name, false) }

func setLinkFlag(name string, up bool) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("ifreq for %s: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("read flags: %w", err)
	}
	if up {
		ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	} else {
		ifr.SetUint16(ifr.Uint16() &^ unix.IFF_UP)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("set flags: %w", err)
	}
	return nil
}

// runDHCP asks for a lease using whichever client the image ships. Doing this in process would mean
// reimplementing DHCP plus route and resolv.conf handling; the clients below already do that correctly
// and one of them is present in every image we target.
func runDHCP() error {
	var lastErr error
	for _, c := range dhcpCandidates() {
		cmd := exec.Command(c.Path, c.Args...) //nolint:gosec // fixed list, no user input
		if out, err := cmd.CombinedOutput(); err == nil {
			logf("dhcp lease acquired via %s", c.Path)
			return nil
		} else {
			lastErr = fmt.Errorf("%s: %w: %s", c.Path, err, out)
		}
	}
	if lastErr == nil {
		return fmt.Errorf("no dhcp client found in the image (tried %d candidates)", len(dhcpCandidates()))
	}
	return lastErr
}

// poweroff asks the kernel to halt the machine. The Kata kernel has no gpio-keys driver, so Apple VF's
// RequestStop is ignored by the guest and this is the only way to shut it down cleanly from the host.
func poweroff() error {
	unix.Sync() // flush anything buffered before the machine goes away
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
