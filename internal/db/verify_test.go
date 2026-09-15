package db

import (
	"context"
	"testing"
)

func TestVerifyIndexes(t *testing.T) {
	cases := []struct {
		name     string
		sessions []string
	}{
		{name: "an empty database has nothing to lose"},
		{name: "one session", sessions: []string{"alpha"}},
		{name: "several sessions", sessions: []string{"alpha", "beta", "gamma"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := onDisk(t)
			if err := Migrate(database); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			for i, name := range tc.sessions {
				if _, err := database.Exec(
					`INSERT INTO sessions (id, name, agent, image, owner_email) VALUES (?, ?, ?, ?, ?)`,
					name+string(rune('0'+i)), name, "claude", "alpine:3.20", "local"); err != nil {
					t.Fatalf("insert %s: %v", name, err)
				}
			}
			if err := VerifyIndexes(context.Background(), database); err != nil {
				t.Errorf("want no error, got %v", err)
			}
		})
	}
}

// A dropped table is the one way to make a lookup fail from a test, and it proves the error is reported
// rather than swallowed.
func TestVerifyIndexesReportsAFailedLookup(t *testing.T) {
	database := onDisk(t)
	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE sessions`); err != nil {
		t.Fatalf("drop sessions: %v", err)
	}
	if err := VerifyIndexes(context.Background(), database); err == nil {
		t.Error("want an error when the table cannot be read")
	}
}

func TestRebuildIndexes(t *testing.T) {
	database := onDisk(t)
	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.Exec(`CREATE INDEX sessions_agent_idx ON sessions(agent)`); err != nil {
		t.Fatalf("create index: %v", err)
	}

	rebuilt, err := RebuildIndexes(context.Background(), database)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if len(rebuilt) != 1 || rebuilt[0] != "sessions_agent_idx" {
		t.Fatalf("rebuilt %v, want [sessions_agent_idx]", rebuilt)
	}
	if err := VerifyIndexes(context.Background(), database); err != nil {
		t.Errorf("verify after rebuild: %v", err)
	}
}

func TestSetUp(t *testing.T) {
	database := onDisk(t)

	ready, err := SetUp(context.Background(), database)
	if err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	if ready {
		t.Error("an untouched database reported a schema")
	}

	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ready, err = SetUp(context.Background(), database)
	if err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	if !ready {
		t.Error("a migrated database reported no schema")
	}
}
