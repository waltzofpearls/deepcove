package repository

import (
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// fakeMigrations is a small helper to build an in-memory migration
// filesystem rooted at "db/migrations/" so the tests look like the
// real embed layout.
func fakeMigrations(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out[filepath.Join("db", "migrations", name)] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func TestMigrate_FreshDB_AppliesAllInOrder(t *testing.T) {
	db := openMemDB(t)
	defer db.Close()

	fs := fakeMigrations(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER);`,
	})

	if err := migrate(db, fs, "db/migrations"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	applied := mustLoadApplied(t, db)
	if !applied["0001_a.sql"] || !applied["0002_b.sql"] {
		t.Errorf("expected both migrations applied, got %v", applied)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := openMemDB(t)
	defer db.Close()

	fs := fakeMigrations(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
	})

	if err := migrate(db, fs, "db/migrations"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := migrate(db, fs, "db/migrations"); err != nil {
		t.Fatalf("second run (should be no-op): %v", err)
	}

	// Exactly one row in schema_migrations.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations row count: want 1, got %d", count)
	}
}

// TestMigrate_RefusesOutOfOrder is the regression test for the bug
// caught in review: if a new migration is added with a name that
// sorts before an already-applied one, refuse rather than silently
// produce a state no fresh deploy could reach.
func TestMigrate_RefusesOutOfOrder(t *testing.T) {
	db := openMemDB(t)
	defer db.Close()

	// First run: apply 0001 and 0003 (simulating a branch that
	// jumped a version number).
	first := fakeMigrations(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
		"0003_c.sql": `CREATE TABLE c (id INTEGER);`,
	})
	if err := migrate(db, first, "db/migrations"); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run: someone merged a branch that adds 0002 (which
	// numerically belongs between 0001 and 0003). This must error.
	second := fakeMigrations(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER);`,
		"0003_c.sql": `CREATE TABLE c (id INTEGER);`,
	})
	err := migrate(db, second, "db/migrations")
	if !errors.Is(err, ErrOutOfOrderMigration) {
		t.Fatalf("want ErrOutOfOrderMigration, got %v", err)
	}

	// And we must NOT have applied 0002 — that would be the bug.
	applied := mustLoadApplied(t, db)
	if applied["0002_b.sql"] {
		t.Errorf("0002 was applied despite gap error — bug")
	}
}

func TestMigrate_FailedMigrationRollsBack(t *testing.T) {
	db := openMemDB(t)
	defer db.Close()

	fs := fakeMigrations(map[string]string{
		"0001_a.sql":   `CREATE TABLE a (id INTEGER);`,
		"0002_bad.sql": `THIS IS NOT VALID SQL;`,
	})

	if err := migrate(db, fs, "db/migrations"); err == nil {
		t.Fatal("expected error from bad migration, got nil")
	}

	applied := mustLoadApplied(t, db)
	if !applied["0001_a.sql"] {
		t.Errorf("0001 should have been applied before the failure")
	}
	if applied["0002_bad.sql"] {
		t.Errorf("0002 should NOT be marked applied (transaction rolled back)")
	}
}
