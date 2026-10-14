//go:build linux

package netguard

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// WatchHostNetworks calls changed whenever this machine's interfaces or addresses change, until ctx ends.
// The kernel tells it, so a network the host joins is seen at once rather than at the next poll.
func WatchHostNetworks(ctx context.Context, changed func()) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("netguard: watch this machine's networks: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		return fmt.Errorf("netguard: watch this machine's networks: %w", err)
	}
	// A network joined before the watch began sent its notice to nobody, so look once now.
	changed()
	buf := make([]byte, 64<<10)
	for {
		// Waits a second at most, so the watch ends soon after ctx does.
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 1000)
		if ctx.Err() != nil {
			return nil
		}
		if err == unix.EINTR || ready == 0 {
			continue
		}
		if err != nil {
			return fmt.Errorf("netguard: watch this machine's networks: %w", err)
		}
		if _, _, err := unix.Recvfrom(fd, buf, 0); err != nil && err != unix.ENOBUFS {
			return fmt.Errorf("netguard: watch this machine's networks: %w", err)
		}
		// One call per batch of messages is enough: the caller reads the whole list again.
		changed()
	}
}
