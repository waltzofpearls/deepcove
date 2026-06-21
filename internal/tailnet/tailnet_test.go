package tailnet

import (
	"testing"

	"tailscale.com/client/local"
)

// TestConfigureLocalClientOmitsAuth pins the busy-loop fix: the in-process tsnet
// LocalClient must omit the local-daemon auth token. Without OmitAuth, every WhoIs/Status runs
// safesocket.LocalTCPPortAndToken, which on macOS forks a subprocess per call; under owner-gate
// request load that fork storm pegged a core. If anyone drops the OmitAuth line, this goes red.
func TestConfigureLocalClientOmitsAuth(t *testing.T) {
	lc := &local.Client{}
	if lc.OmitAuth {
		t.Fatal("precondition: a fresh local.Client should default OmitAuth=false")
	}
	configureLocalClient(lc)
	if !lc.OmitAuth {
		t.Error("configureLocalClient must set OmitAuth=true to avoid per-request subprocess forks on macOS")
	}
}
