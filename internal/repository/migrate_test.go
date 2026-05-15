package repository

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"testing/fstest"

	_ "github.com/marcboeker/go-duckdb/v2"
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
			defer db.Close()

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
