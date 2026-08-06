//go:build darwin

package applevf

import (
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
)

// serveGuestConfig answers this VM's boot configuration request. One listener per VM, so a connection
// already identifies the session and the guest never has to name itself.
func serveGuestConfig(m *machine, cfg guestcfg.Config) error {
	device, err := m.socketDevice()
	if err != nil {
		return err
	}
	listener, err := device.Listen(guestcfg.Port)
	if err != nil {
		return fmt.Errorf("applevf: listen for the guest config service on vsock port %d: %w", guestcfg.Port, err)
	}
	m.cfgListener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener closed with the VM
			}
			go func() { _ = guestcfg.Serve(conn, cfg) }()
		}
	}()
	return nil
}
