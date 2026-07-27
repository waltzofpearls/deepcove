//go:build !linux

package guestexec

import (
	"context"
	"errors"
)

// ListenAndServe only runs inside the Linux guest, where vsock exists. The stub keeps the package
// building on the macOS host, which imports the client and protocol from the same package.
func ListenAndServe(context.Context) error {
	return errors.New("guestexec: the exec service listens on vsock inside the Linux guest only")
}
