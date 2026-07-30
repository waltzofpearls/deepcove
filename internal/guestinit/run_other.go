//go:build !linux

package guestinit

import "errors"

// Run only functions as PID 1 inside a Linux guest. This stub lets the package build on the macOS host
// (where the driver imports the shared constants and plan logic) without pulling Linux only syscalls
// into the host binary.
func Run() error {
	return errors.New("guestinit: the deepcove guest init runs inside the Linux VM only")
}
