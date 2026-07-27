//go:build !linux

package guestctl

import (
	"context"
	"errors"
)

// ListenAndServe only runs inside the Linux guest, where vsock exists. The stub keeps the package
// building on the macOS host, which uses Send and the wire types from the same package.
func ListenAndServe(context.Context, Handler) error {
	return errors.New("guestctl: the control service listens on vsock inside the Linux guest only")
}
