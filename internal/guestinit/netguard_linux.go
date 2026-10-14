//go:build linux

package guestinit

import "strings"

// allowLabel describes the holes in the block for the boot log, so a session never opens one silently.
func allowLabel(allow []string) string {
	if len(allow) == 0 {
		return ""
	}
	return ", except " + strings.Join(allow, ", ")
}
