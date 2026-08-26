package tailnet

import (
	"bytes"
	"log"
	"os"
	"strings"
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
		{
			name:     "drops tailscale's verbose lines by default",
			format:   "[v1] peer keys: %s",
			args:     []any{"[P3pgw]"},
			wantDrop: true,
		},
		{
			// Tailscale's other marker, where the level rides in a JSON tag with a NUL in it.
			name:     "drops the json tagged verbose lines too",
			format:   "control: [v\x00JSON]1{\"controltime\":%q}",
			args:     []any{"2026-08-26T09:15:47Z"},
			wantDrop: true,
		},
		{
			name:     "keeps connection events, which carry no marker",
			format:   "magicsock: adding connection to derp-4 for %s",
			args:     []any{"[P3pgw]"},
			wantDrop: false,
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

// At debug level the verbose lines are the point, so they have to come through.
func TestSetVerbosePassesTailscaleDetailThrough(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	SetVerbose(true)
	t.Cleanup(func() {
		SetVerbose(false)
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	filteredLogf("[v1] peer keys: %s", "[P3pgw]")
	if got := buf.String(); !strings.Contains(got, "peer keys") {
		t.Errorf("verbose line was dropped at debug level, got %q", got)
	}

	// The named noise stays out whatever the level: it is not detail, it is repetition.
	buf.Reset()
	filteredLogf("To start this tsnet server, restart with TS_AUTHKEY set, or go to: %s", "https://example")
	if got := buf.String(); got != "" {
		t.Errorf("auth reminder came back at debug level: %q", got)
	}
}
