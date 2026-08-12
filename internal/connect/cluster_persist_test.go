package connect

import (
	"context"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestNodeClusterPersistence(t *testing.T) {
	database := db.OpenForTest(t)

	// Empty to start.
	if got, err := loadNodeClusters(database); err != nil || len(got) != 0 {
		t.Fatalf("initial loadNodeClusters: got (%v, %v), want (empty, nil)", got, err)
	}

	m := NewManager(context.Background(), t.TempDir(), database, nil, nil)
	m.cacheCluster("nEREBOR", "dev")
	m.cacheCluster("", "prod")        // empty id: ignored, not persisted
	m.cacheCluster("nOTHER", "")      // empty label: ignored, not persisted
	m.cacheCluster("nEREBOR", "prod") // changed: last write wins

	got, err := loadNodeClusters(database)
	if err != nil {
		t.Fatalf("loadNodeClusters: %v", err)
	}
	if len(got) != 1 || got["nEREBOR"].cluster != "prod" {
		t.Fatalf("persisted = %v, want {nEREBOR: prod}", got)
	}

	// a fresh Manager (restart) seeds its cache from the table
	restarted := NewManager(context.Background(), t.TempDir(), database, nil, nil)
	if got := restarted.cachedCluster("nEREBOR"); got != "prod" {
		t.Errorf("after restart cachedCluster = %q, want prod", got)
	}
}
