package repository

import (
	"database/sql"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// openMemDB returns an in-memory DuckDB handle for tests. go-duckdb
// treats an empty DSN as in-memory; the handle is closed by the
// caller's defer.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open in-memory duckdb: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping in-memory duckdb: %v", err)
	}
	return db
}

func mustLoadApplied(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	applied, err := loadApplied(db)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	return applied
}
