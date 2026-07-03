package tailnet

import (
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// peerMap puts peers into the key.NodePublic-keyed map ipnstate.Status uses. The keys are random
// (the filter only reads values), and randomizing also guards against any accidental order reliance.
func peerMap(peers ...*ipnstate.PeerStatus) map[key.NodePublic]*ipnstate.PeerStatus {
	out := make(map[key.NodePublic]*ipnstate.PeerStatus, len(peers))
	for _, peer := range peers {
		out[key.NewNode().Public()] = peer
	}
	return out
}

func TestFilterDeepcoveNodes(t *testing.T) {
	const me = tailcfg.UserID(1)
	const other = tailcfg.UserID(2)

	self := &ipnstate.PeerStatus{
		ID: "self-id", HostName: "deepcove-laptop", DNSName: "deepcove-laptop.tail.ts.net.",
		OS: "macOS", UserID: me,
	}

	cases := []struct {
		name      string
		status    *ipnstate.Status
		wantHosts []string // expected NodeInfo.Hostname order; first must be self
	}{
		{name: "nil status", status: nil, wantHosts: nil},
		{name: "no self", status: &ipnstate.Status{}, wantHosts: nil},
		{name: "self only", status: &ipnstate.Status{Self: self}, wantHosts: []string{"deepcove-laptop"}},
		{
			name: "includes same-user deepcove peers, sorted; excludes others",
			status: &ipnstate.Status{
				Self: self,
				Peer: peerMap(
					&ipnstate.PeerStatus{ID: "p1", HostName: "deepcove-server", UserID: me, Online: true},
					&ipnstate.PeerStatus{ID: "p2", HostName: "deepcove-cloud", UserID: me, Online: false},
					// excluded: same user but not a deepcove node.
					&ipnstate.PeerStatus{ID: "p3", HostName: "alices-phone", UserID: me, Online: true},
					// excluded: a deepcove node owned by a different user.
					&ipnstate.PeerStatus{ID: "p4", HostName: "deepcove-bob", UserID: other, Online: true},
					// excluded: the in-browser tailscale-web client (deepcove- prefix + same user, but
					// it's a client compiled GOOS=js, not a hosting node).
					&ipnstate.PeerStatus{ID: "p5", HostName: "deepcove-web", UserID: me, OS: "js", Online: true},
				),
			},
			// self first, then peers sorted by hostname (cloud < server).
			wantHosts: []string{"deepcove-laptop", "deepcove-cloud", "deepcove-server"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterDeepcoveNodes(tc.status)
			if len(got) != len(tc.wantHosts) {
				t.Fatalf("got %d nodes %v, want %d %v", len(got), hostnames(got), len(tc.wantHosts), tc.wantHosts)
			}
			for i, want := range tc.wantHosts {
				if got[i].Hostname != want {
					t.Errorf("node[%d].Hostname = %q, want %q", i, got[i].Hostname, want)
				}
			}
			if len(got) > 0 {
				if !got[0].Self {
					t.Errorf("first node must be Self, got %+v", got[0])
				}
				for _, node := range got[1:] {
					if node.Self {
						t.Errorf("only the first node may be Self, got self=true for %q", node.Hostname)
					}
				}
			}
		})
	}
}

func TestFilterWebNodes(t *testing.T) {
	const me = tailcfg.UserID(1)
	const other = tailcfg.UserID(2)

	// Self is a hosting node (real OS), never a web node.
	self := &ipnstate.PeerStatus{ID: "self-id", HostName: "deepcove-laptop", OS: "macOS", UserID: me}

	cases := []struct {
		name      string
		status    *ipnstate.Status
		wantHosts []string
	}{
		{name: "nil status", status: nil, wantHosts: nil},
		{name: "self only, no web nodes", status: &ipnstate.Status{Self: self}, wantHosts: nil},
		{
			name: "online same-user js peers only, sorted; excludes offline/other/non-js",
			status: &ipnstate.Status{
				Self: self,
				Peer: peerMap(
					&ipnstate.PeerStatus{ID: "w1", HostName: "deepcove-web-3", UserID: me, OS: "js", Online: true},
					&ipnstate.PeerStatus{ID: "w2", HostName: "deepcove-web", UserID: me, OS: "js", Online: true},
					// excluded: offline web node (ephemeral, stale).
					&ipnstate.PeerStatus{ID: "w3", HostName: "deepcove-web-2", UserID: me, OS: "js", Online: false},
					// excluded: a hosting node (not js).
					&ipnstate.PeerStatus{ID: "s1", HostName: "deepcove-server", UserID: me, OS: "linux", Online: true},
					// excluded: another user's web node.
					&ipnstate.PeerStatus{ID: "w4", HostName: "deepcove-web-bob", UserID: other, OS: "js", Online: true},
				),
			},
			// no self; online js peers sorted by hostname (deepcove-web < deepcove-web-3).
			wantHosts: []string{"deepcove-web", "deepcove-web-3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterWebNodes(tc.status)
			if len(got) != len(tc.wantHosts) {
				t.Fatalf("got %d nodes %v, want %d %v", len(got), hostnames(got), len(tc.wantHosts), tc.wantHosts)
			}
			for i, want := range tc.wantHosts {
				if got[i].Hostname != want {
					t.Errorf("node[%d].Hostname = %q, want %q", i, got[i].Hostname, want)
				}
			}
			for _, node := range got {
				if node.Self {
					t.Errorf("web nodes must never be Self, got self=true for %q", node.Hostname)
				}
			}
		})
	}
}

func hostnames(nodes []NodeInfo) []string {
	out := make([]string, len(nodes))
	for i, node := range nodes {
		out[i] = node.Hostname
	}
	return out
}
