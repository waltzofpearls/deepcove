package db

import (
	"database/sql"
	"testing"
)

// OpenForTest returns a migrated in-memory database, closed when the test ends.
//
// Here rather than in each package's test helpers so no test has to know what the schema looks like:
// a hand written copy drifts from the migrations, and then tests pass against a table nobody runs.
func OpenForTest(tb testing.TB) *sql.DB {
	tb.Helper()
	// An empty DSN is in-memory, so each test gets its own database and nothing lands on disk.
	database, err := sql.Open("duckdb", "")
	if err != nil {
		tb.Fatalf("open in-memory duckdb: %v", err)
	}
	tb.Cleanup(func() { _ = database.Close() })
	if err := Migrate(database); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return database
}
