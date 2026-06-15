package connect

import (
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

func TestOwnerPersistence(t *testing.T) {
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Unclaimed node -> nil.
	if o, err := loadOwner(database); err != nil || o != nil {
		t.Fatalf("initial loadOwner: want (nil, nil), got (%+v, %v)", o, err)
	}

	// First claim persists.
	first := &tailnet.Identity{UserID: 42, LoginName: "rollie@example.com"}
	if err := saveOwner(database, "nABC", first); err != nil {
		t.Fatalf("saveOwner: %v", err)
	}
	o, err := loadOwner(database)
	if err != nil || o == nil {
		t.Fatalf("loadOwner after save: %+v, %v", o, err)
	}
	if o.UserID != first.UserID || o.LoginName != first.LoginName {
		t.Errorf("owner: want %+v, got %+v", first, o)
	}

	// Read-once: a second claim with a different identity is a no-op.
	if err := saveOwner(database, "nXYZ", &tailnet.Identity{UserID: 99, LoginName: "other@example.com"}); err != nil {
		t.Fatalf("second saveOwner: %v", err)
	}
	o, _ = loadOwner(database)
	if o.UserID != first.UserID {
		t.Errorf("read-once violated: owner changed to %+v", o)
	}
}
