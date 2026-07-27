//go:build darwin

package applevf

import "github.com/waltzofpearls/deepcove/internal/credbroker"

// serveGuestCredentials starts the host side credential broker for a running VM.
func serveGuestCredentials(m *machine, store credbroker.Store) error {
	return m.serveCredentials(store)
}
