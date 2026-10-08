package db

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"testing/fstest"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/waltzofpearls/deepcove"
)

// fakeMigrations builds an in-memory migration filesystem rooted at
// "db/migrations/" so the tests look like the real embed layout.
func fakeMigrations(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out[filepath.Join("db", "migrations", name)] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

// TestMigrate exercises every path through the migrator: fresh
// install, idempotent re-runs, the load-bearing out-of-order
// detection (the bug we caught in review), and rollback when one
// file's SQL fails. priorFiles seeds the DB with an earlier
// migration run before the call under test, mimicking a returning
// operator who picked up new files.
func TestMigrate(t *testing.T) {
	tests := []struct {
		name        string
		priorFiles  map[string]string // applied first to set up initial state; nil = skip
		files       map[string]string // applied in the call under test
		wantErrIs   error             // if non-nil, errors.Is(err, wantErrIs) must hold
		wantAnyErr  bool              // if true, expect a non-nil error of any shape
		wantApplied []string          // expected versions in schema_migrations after all runs
	}{
		{
			name: "fresh DB applies all in order",
			files: map[string]string{
				"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
				"0002_b.sql": `CREATE TABLE b (id INTEGER);`,
			},
			wantApplied: []string{"0001_a.sql", "0002_b.sql"},
		},
		{
			name: "idempotent re-run is a no-op",
			priorFiles: map[string]string{
				"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
			},
			files: map[string]string{
				"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
			},
			wantApplied: []string{"0001_a.sql"},
		},
		{
			name: "refuses out-of-order migration with newer file behind already-applied",
			priorFiles: map[string]string{
				"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
				"0003_c.sql": `CREATE TABLE c (id INTEGER);`,
			},
			files: map[string]string{
				"0001_a.sql": `CREATE TABLE a (id INTEGER);`,
				"0002_b.sql": `CREATE TABLE b (id INTEGER);`,
				"0003_c.sql": `CREATE TABLE c (id INTEGER);`,
			},
			wantErrIs:   ErrOutOfOrderMigration,
			wantApplied: []string{"0001_a.sql", "0003_c.sql"},
		},
		{
			name: "failed migration rolls back; earlier migrations stay applied",
			files: map[string]string{
				"0001_a.sql":   `CREATE TABLE a (id INTEGER);`,
				"0002_bad.sql": `THIS IS NOT VALID SQL;`,
			},
			wantAnyErr:  true,
			wantApplied: []string{"0001_a.sql"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMemDB(t)
			defer func() { _ = db.Close() }()

			if tc.priorFiles != nil {
				if err := migrate(db, fakeMigrations(tc.priorFiles), "db/migrations"); err != nil {
					t.Fatalf("priorFiles migrate failed: %v", err)
				}
			}

			gotErr := migrate(db, fakeMigrations(tc.files), "db/migrations")
			assertErr(t, gotErr, tc.wantErrIs, tc.wantAnyErr)

			if !slices.Equal(sortedAppliedVersions(t, db), tc.wantApplied) {
				t.Errorf("applied versions: want %v, got %v", tc.wantApplied, sortedAppliedVersions(t, db))
			}
		})
	}
}

// assertErr is the table-test verdict for the (wantErrIs, wantAnyErr,
// got) tuple.
func assertErr(t *testing.T, got, wantIs error, wantAny bool) {
	t.Helper()
	switch {
	case wantIs != nil:
		if !errors.Is(got, wantIs) {
			t.Errorf("want errors.Is(err, %v), got %v", wantIs, got)
		}
	case wantAny:
		if got == nil {
			t.Errorf("want any error, got nil")
		}
	default:
		if got != nil {
			t.Errorf("want nil error, got %v", got)
		}
	}
}

// sortedAppliedVersions returns the schema_migrations contents as a
// sorted slice, suitable for slices.Equal comparison in tests.
func sortedAppliedVersions(t *testing.T, db *sql.DB) []string {
	t.Helper()
	applied := mustLoadApplied(t, db)
	out := make([]string, 0, len(applied))
	for v := range applied {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// onDisk is a file-backed database. The transaction crash this directive exists for only happens on a
// real file, so an in-memory database would let the bug straight back in.
func onDisk(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("duckdb", filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestMigrateWithoutATransaction(t *testing.T) {
	database := onDisk(t)

	files := fakeMigrations(map[string]string{
		"0001_init.sql": `CREATE TABLE t (id TEXT PRIMARY KEY, owner TEXT, old_name TEXT);
			CREATE INDEX t_owner_idx ON t(owner);
			INSERT INTO t VALUES ('one', 'local', 'kept');`,
		// Dropping an index and renaming a column in one transaction aborts the DuckDB process, so this
		// is the shape the directive exists for.
		"0002_rename.sql": `-- deepcove:no-transaction
			DROP INDEX IF EXISTS t_owner_idx;
			ALTER TABLE t RENAME COLUMN old_name TO new_name;
			CREATE INDEX IF NOT EXISTS t_owner_idx ON t(owner);`,
	})
	if err := migrate(database, files, migrationsDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var value string
	if err := database.QueryRow(`SELECT new_name FROM t WHERE id = 'one'`).Scan(&value); err != nil {
		t.Fatalf("read the renamed column: %v", err)
	}
	if value != "kept" {
		t.Errorf("value %q, want it carried across the rename", value)
	}
	applied := mustLoadApplied(t, database)
	if !applied["0002_rename.sql"] {
		t.Error("the migration was not recorded")
	}
}

// Without the directive a failure has to leave nothing behind, which is the whole reason the default
// stays transactional.
func TestMigrateRollsBackWithoutTheDirective(t *testing.T) {
	database := onDisk(t)

	files := fakeMigrations(map[string]string{
		"0001_broken.sql": `CREATE TABLE a (id TEXT);
			CREATE TABLE b (id TEXT);
			THIS IS NOT SQL;`,
	})
	if err := migrate(database, files, migrationsDir); err == nil {
		t.Fatal("migrate: want an error")
	}

	var tables int
	if err := database.QueryRow(
		`SELECT count(*) FROM duckdb_tables() WHERE table_name IN ('a', 'b')`).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tables != 0 {
		t.Errorf("%d tables survived a failed migration, want none", tables)
	}
	if applied := mustLoadApplied(t, database); applied["0001_broken.sql"] {
		t.Error("a failed migration was recorded as applied")
	}
}

// The real rename, over a database holding a session, which is the case that crashed.
func TestSessionSandboxIDMigration(t *testing.T) {
	database := onDisk(t)
	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sessions (id, name, agent, image, owner_email, sandbox_id)
		VALUES ('019ff000', 'placement', 'claude', 'alpine:3.20', 'local', 'applevf-abc123')`); err != nil {
		t.Fatalf("insert a session: %v", err)
	}

	var sandboxID string
	if err := database.QueryRow(`SELECT sandbox_id FROM sessions WHERE id = '019ff000'`).Scan(&sandboxID); err != nil {
		t.Fatalf("read sandbox_id: %v", err)
	}
	if sandboxID != "applevf-abc123" {
		t.Errorf("sandbox id %q, want applevf-abc123", sandboxID)
	}
	var indexes int
	if err := database.QueryRow(
		`SELECT count(*) FROM duckdb_indexes() WHERE table_name = 'sessions'`).Scan(&indexes); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	// 0016 dropped them all: a table this small never needed one, and a lost index hides every row.
	if indexes != 0 {
		t.Errorf("%d indexes on sessions, want none", indexes)
	}
}

func TestSessionLoginModeMigration(t *testing.T) {
	database := onDisk(t)
	before := fstest.MapFS{}
	names, err := listMigrations(deepcove.Migrations(), migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name < "0019" {
			body, _ := fs.ReadFile(deepcove.Migrations(), migrationsDir+"/"+name)
			before[migrationsDir+"/"+name] = &fstest.MapFile{Data: body}
		}
	}
	if err := migrate(database, before, migrationsDir); err != nil {
		t.Fatalf("migrate to 0018: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sessions (id, name, agent, image, owner_email, runtime, use_llm_proxy) VALUES
		('proxied', 'a', 'claude', '', 'local', 'applevf', true),
		('no-sandbox', 'b', 'claude', '', 'local', 'sandboxless', false),
		('vm', 'c', 'claude', '', 'local', 'applevf', false)`); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want := map[string]string{"proxied": "llm_proxy", "no-sandbox": "in_session", "vm": "shared"}
	for id, mode := range want {
		var got string
		if err := database.QueryRow(`SELECT login_mode FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != mode {
			t.Errorf("%s login_mode = %q, want %q", id, got, mode)
		}
	}
}
