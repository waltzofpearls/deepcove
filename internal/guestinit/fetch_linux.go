//go:build linux

package guestinit

import (
	"fmt"

	"github.com/mdlayher/vsock"
	"github.com/waltzofpearls/deepcove/internal/guestcfg"
)

// hostContextID is the well known vsock address of the host, which the guest dials to reach the
// services the host runs for it.
const hostContextID uint32 = 2

// fetchConfig asks the host for this session's configuration. It is fatal if it fails: without an
// entrypoint the guest has nothing to run, and a session that silently starts the wrong thing is worse
// than one that reports the problem.
//
// The image cannot carry this because it is shared by every session using the same image, while the
// entrypoint is per session.
func fetchConfig() (guestcfg.Config, error) {
	conn, err := vsock.Dial(hostContextID, guestcfg.Port, nil)
	if err != nil {
		return guestcfg.Config{}, fmt.Errorf("dial host config service: %w", err)
	}
	defer func() { _ = conn.Close() }()

	cfg, err := guestcfg.Fetch(conn)
	if err != nil {
		return guestcfg.Config{}, err
	}
	if len(cfg.Entrypoint) == 0 {
		return guestcfg.Config{}, fmt.Errorf("host sent no entrypoint to run")
	}
	return cfg, nil
}
