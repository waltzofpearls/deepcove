package connect

import (
	"context"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// TestEnabledMarker pins the auto-start marker plumbing the wizard relies on: a fresh manager is not
// enabled, markEnabled persists it, and clearEnabled removes it. The marker is what AutoStartIfEnabled
// reads on boot, and (post-fix) it's only written once a connection actually reaches Running, so an
// abandoned sign-in leaves nothing behind to auto-start a half-connected, log-spamming node.
func TestEnabledMarker(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), "", nil, nil, nil)

	if m.isEnabled() {
		t.Fatal("a fresh manager must not be enabled")
	}
	if err := m.markEnabled(); err != nil {
		t.Fatalf("markEnabled: %v", err)
	}
	if !m.isEnabled() {
		t.Error("expected enabled after markEnabled")
	}
	m.clearEnabled()
	if m.isEnabled() {
		t.Error("expected not enabled after clearEnabled")
	}
}

func TestWithVersions(t *testing.T) {
	cases := []struct {
		name string
		node tailnet.NodeInfo
		want string
	}{
		{name: "this node uses its build", node: tailnet.NodeInfo{StableNodeID: "n-self", Self: true}, want: "v1.3.0"},
		{name: "this node with no tailnet yet", node: tailnet.NodeInfo{Self: true}, want: "v1.3.0"},
		{name: "a peer that gossiped", node: tailnet.NodeInfo{StableNodeID: "n-peer"}, want: "v1.0.2"},
		{name: "a peer that has not said", node: tailnet.NodeInfo{StableNodeID: "n-quiet"}, want: ""},
	}
	versions := map[string]string{"n-self": "v0.9.0", "n-peer": "v1.0.2"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withVersions([]tailnet.NodeInfo{tc.node}, versions, "v1.3.0")
			if got[0].Version != tc.want {
				t.Errorf("version = %q, want %q", got[0].Version, tc.want)
			}
		})
	}
}
