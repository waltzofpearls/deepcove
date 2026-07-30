//go:build !darwin

package applevf

import (
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func serveGuestConfig(*machine, guestcfg.Config) error {
	return fmt.Errorf("applevf: the guest config service requires macOS: %w", sandbox.ErrNotSupported)
}
