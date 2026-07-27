//go:build !darwin

package applevf

import (
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// serveGuestCredentials is macOS only, like the rest of the Apple VF runtime.
func serveGuestCredentials(*machine, credbroker.Store) error {
	return fmt.Errorf("applevf: the credential broker requires macOS: %w", sandbox.ErrNotSupported)
}
