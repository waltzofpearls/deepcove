//go:build !linux

package netguard

import (
	"context"
	"errors"
)

// WatchHostNetworks is Linux only: the Mac VM network reads the host's networks for every connection.
func WatchHostNetworks(context.Context, func()) error {
	return errors.New("netguard: watching this machine's networks is only done on Linux")
}
