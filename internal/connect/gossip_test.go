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

func TestObserveClusterFreshestWins(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), nil, nil, nil)
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t1.Add(time.Hour)

	m.observeCluster("n1", "default", t1)
	if got := m.cachedCluster("n1"); got != "default" {
		t.Fatalf("initial = %q, want default", got)
	}
	m.observeCluster("n1", "dev", t0) // older evidence for a different value
	if got := m.cachedCluster("n1"); got != "default" {
		t.Errorf("older observation won: %q, want default", got)
	}
	m.observeCluster("n1", "dev", t2) // fresher evidence for a different value
	if got := m.cachedCluster("n1"); got != "dev" {
		t.Errorf("fresher observation ignored: %q, want dev", got)
	}

	m.observeCluster("", "prod", t2)
	m.observeCluster("n2", "", t2)
	if got := m.cachedCluster("n2"); got != "" {
		t.Errorf("empty label cached: %q", got)
	}
}

func TestObserveClusterKeepsChangeTime(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), nil, nil, nil)
	t1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	tMid := t1.Add(30 * time.Minute)
	t2 := t1.Add(time.Hour)

	m.observeCluster("n1", "dev", t1)
	m.observeCluster("n1", "dev", t2) // same value, later time: must NOT bump the timestamp to t2
	// A differing value observed between t1 and t2 must win, proving the stored timestamp stayed at t1.
	m.observeCluster("n1", "default", tMid)
	if got := m.cachedCluster("n1"); got != "default" {
		t.Errorf("same-value re-observe bumped the timestamp: %q, want default", got)
	}
}

func TestNodeClusterTimestampSurvivesReload(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Far-apart times so any TIMESTAMP timezone interpretation on read can't flip the ordering.
	early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	m := NewManager(context.Background(), t.TempDir(), database, nil, nil)
	m.observeCluster("n1", "dev", late)

	restarted := NewManager(context.Background(), t.TempDir(), database, nil, nil)
	if got := restarted.cachedCluster("n1"); got != "dev" {
		t.Fatalf("after reload = %q, want dev", got)
	}
	// The reloaded observed_at must still be `late`, so an older gossiped value can't override it.
	restarted.observeCluster("n1", "default", early)
	if got := restarted.cachedCluster("n1"); got != "dev" {
		t.Errorf("older observation overrode reloaded value (observed_at not persisted): %q", got)
	}
}

func TestKnownClustersRoundTrip(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), nil, nil, nil)
	at := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	m.observeCluster("n1", "dev", at)

	srv := httptest.NewServer(http.HandlerFunc(m.handleKnownClusters))
	defer srv.Close()

	dns := strings.TrimPrefix(srv.URL, "http://")
	got := fetchKnownClusters(context.Background(), srv.Client(), dns)
	if got["n1"].Cluster != "dev" || !got["n1"].ObservedAt.Equal(at) {
		t.Errorf("round-trip n1 = %+v, want {dev, %v}", got["n1"], at)
	}
}
