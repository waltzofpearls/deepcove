package connect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestMergeGossipValue(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	recent := time.Now()

	cases := []struct {
		name    string
		first   *GossipTopicValue // seeded, or nil for an empty cache
		value   string
		at      time.Time
		want    string
		wantAt  time.Time
		skipped bool // the merge should change nothing
	}{
		{name: "first value is kept", value: "a", at: recent, want: "a", wantAt: recent},
		{
			name:  "newer wins",
			first: &GossipTopicValue{Value: "a", SeenAt: old},
			value: "b", at: recent, want: "b", wantAt: recent,
		},
		{
			name:  "older loses",
			first: &GossipTopicValue{Value: "a", SeenAt: recent},
			value: "b", at: old, want: "a", wantAt: recent, skipped: true,
		},
		{
			name:  "the same value keeps its original stamp",
			first: &GossipTopicValue{Value: "a", SeenAt: old},
			value: "a", at: recent, want: "a", wantAt: old, skipped: true,
		},
		{
			name:  "an empty value retracts, because a node hosting nothing has to be able to say so",
			first: &GossipTopicValue{Value: "a", SeenAt: old},
			value: "", at: recent, want: "", wantAt: recent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{}
			if tc.first != nil {
				m.mergeGossipValue("hosts", "n1", tc.first.Value, tc.first.SeenAt)
			}
			m.mergeGossipValue("hosts", "n1", tc.value, tc.at)

			got := m.gossipValues["hosts"]["n1"]
			if got.Value != tc.want {
				t.Errorf("value = %q, want %q", got.Value, tc.want)
			}
			if !got.SeenAt.Equal(tc.wantAt) {
				t.Errorf("seen_at = %v, want %v", got.SeenAt, tc.wantAt)
			}
		})
	}
}

func TestGossipValuesSurviveAReload(t *testing.T) {
	database := db.OpenForTest(t)
	seen := time.Now().UTC().Truncate(time.Second)
	if err := saveGossipValue(database, "hosts", "n1", GossipTopicValue{Value: "s-1,s-2", SeenAt: seen}); err != nil {
		t.Fatalf("saveGossipValue: %v", err)
	}

	// An offline node's last word exists nowhere else, so a restart must not forget it.
	loaded, err := loadGossipValues(database)
	if err != nil {
		t.Fatalf("loadGossipValues: %v", err)
	}
	got := loaded["hosts"]["n1"]
	if got.Value != "s-1,s-2" {
		t.Errorf("value = %q, want the saved one", got.Value)
	}
	if !got.SeenAt.Equal(seen) {
		t.Errorf("seen_at = %v, want %v", got.SeenAt, seen)
	}
}

func TestKnownGossipValuesStampsThisNodesTopics(t *testing.T) {
	m := &Manager{}
	m.RegisterGossipTopic("hosts", func() (string, bool) { return "s-1", true })
	m.mergeGossipValue("hosts", "peer", "s-9", time.Now().Add(-time.Hour))

	known := m.knownGossipValues()
	if known["hosts"]["peer"].Value != "s-9" {
		t.Errorf("a peer's value should be served on, got %+v", known["hosts"]["peer"])
	}
	// Self has no stable node id without a tailnet, so its own value cannot be stamped here. What
	// matters is that serving a peer's value does not depend on that.
	if len(known["hosts"]) != 1 {
		t.Errorf("known = %+v, want just the peer", known["hosts"])
	}
}

func TestFetchKnownGossipGivesUpOnASilentPeer(t *testing.T) {
	hang := make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	defer func() {
		close(hang)
		peer.Close()
	}()

	start := time.Now()
	got := fetchKnownGossip(context.Background(), peer.Client(), strings.TrimPrefix(peer.URL, "http://"))

	if got != nil {
		t.Errorf("got %v, want nil from a peer that never answers", got)
	}
	if waited := time.Since(start); waited > gossipFetchTimeout*2 {
		t.Errorf("waited %s, want it to give up near %s", waited, gossipFetchTimeout)
	}
}
