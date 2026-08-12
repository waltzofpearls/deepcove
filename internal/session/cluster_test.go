package session

import (
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestClusterStore(t *testing.T) {
	if got := NewClusterStore(nil, "").Name(); got != DefaultCluster {
		t.Errorf("empty name -> %q, want %q", got, DefaultCluster)
	}
	cs := NewClusterStore(nil, "prod")
	if cs.Name() != "prod" {
		t.Errorf("Name = %q, want prod", cs.Name())
	}
	if err := cs.Set("dev"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if cs.Name() != "dev" {
		t.Errorf("after Set = %q, want dev", cs.Name())
	}
	_ = cs.Set("")
	if cs.Name() != DefaultCluster {
		t.Errorf("Set empty -> %q, want %q", cs.Name(), DefaultCluster)
	}
	var nilStore *ClusterStore
	if nilStore.Name() != DefaultCluster {
		t.Errorf("nil store Name -> %q, want %q", nilStore.Name(), DefaultCluster)
	}
}

func TestLoadSaveCluster(t *testing.T) {
	database := db.OpenForTest(t)

	// Unset reads back empty (the caller falls back to the default).
	if got, err := LoadCluster(database); err != nil || got != "" {
		t.Fatalf("unset LoadCluster = %q, err %v; want empty", got, err)
	}

	if err := SaveCluster(database, "prod"); err != nil {
		t.Fatalf("SaveCluster: %v", err)
	}
	if got, _ := LoadCluster(database); got != "prod" {
		t.Errorf("LoadCluster = %q, want prod", got)
	}

	// Upsert overwrites rather than erroring on the primary key.
	if err := SaveCluster(database, "dev"); err != nil {
		t.Fatalf("SaveCluster upsert: %v", err)
	}
	if got, _ := LoadCluster(database); got != "dev" {
		t.Errorf("after upsert LoadCluster = %q, want dev", got)
	}
}
