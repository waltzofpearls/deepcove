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
// leaving a session without the access the user thought they granted.
func (r *repeatedFlag) validateAddrs() error {
	for _, entry := range *r {
		if strings.Contains(entry, "/") {
			if _, err := netip.ParsePrefix(entry); err != nil {
				return fmt.Errorf("%q is not a CIDR: %w", entry, err)
			}
			continue
		}
		if _, err := netip.ParseAddr(entry); err != nil {
			return fmt.Errorf("%q is not an IP address or CIDR: %w", entry, err)
		}
	}
	return nil
}
