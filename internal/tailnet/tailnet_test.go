package tailnet

import (
	"bytes"
	"log"
	"os"
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

// TestFilteredLogf pins that tsnet's repetitive auth reminder is dropped while every other log line
// still passes through. The reminder spams every few seconds for the whole time a node is unauthed.
func TestFilteredLogf(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	cases := []struct {
		name     string
		format   string
		args     []any
		wantDrop bool
	}{
		{
			name:     "drops auth reminder",
			format:   "To start this tsnet server, restart with TS_AUTHKEY set, or go to: %s",
			args:     []any{"https://login.tailscale.com/a/4b6130301d70c"},
			wantDrop: true,
		},
		{
			name:     "drops netstack conns-in-flight noise",
			format:   "netstack: decrementing connsInFlightByClient[%s] because the packet was not handled; new value is %d",
			args:     []any{"100.86.239.55", 0},
			wantDrop: true,
		},
		{name: "keeps status line", format: "tsnet running: %s", args: []any{"deepcove-host"}, wantDrop: false},
		{name: "keeps unrelated line", format: "some diagnostic", wantDrop: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			filteredLogf(tc.format, tc.args...)
			got := buf.String()
			if tc.wantDrop && got != "" {
				t.Errorf("expected line dropped, got %q", got)
			}
			if !tc.wantDrop && got == "" {
				t.Error("expected line to pass through, got nothing")
			}
		})
	}
}
