//go:build !linux && !darwin && !windows

package localpeer

import "net/netip"

func sameUser(netip.AddrPort, netip.AddrPort) (bool, error) {
	return false, ErrUnsupported
}
