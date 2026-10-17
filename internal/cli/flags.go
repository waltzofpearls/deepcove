package cli

import (
	"fmt"
	"net/netip"
	"strings"
)

// repeatedFlag collects a flag the user gives more than once, e.g. --sandbox-can-reach a
// --sandbox-can-reach b.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatedFlag) Set(value string) error {
	// Commas as well as repeats, so a whole list fits in one flag.
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return fmt.Errorf("empty value")
		}
		*r = append(*r, entry)
	}
	return nil
}

// validateAddrs checks every entry is an IP or a CIDR, so a typo fails at startup rather than silently
// leaving a session without the access the user thought they granted. IPv6 is refused on macOS, where the
// VM network deepcove runs carries IPv4 only, so such an entry could never be reached.
func (r *repeatedFlag) validateAddrs(goos string) error {
	for _, entry := range *r {
		var addr netip.Addr
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return fmt.Errorf("%q is not a CIDR: %w", entry, err)
			}
			addr = prefix.Addr()
		} else {
			parsed, err := netip.ParseAddr(entry)
			if err != nil {
				return fmt.Errorf("%q is not an IP address or CIDR: %w", entry, err)
			}
			addr = parsed
		}
		if goos == "darwin" && addr.Is6() {
			return fmt.Errorf("%q is IPv6, which a sandbox on macOS cannot reach: its network is IPv4 only", entry)
		}
	}
	return nil
}
