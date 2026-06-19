// Package db owns deepcove's local DuckDB store. Every node keeps its own database under
// cfg.DataDir/deepcove.duckdb; there is no cross-node replication (per-session sharing only).
//
// Scope is intentionally narrow: connection lifecycle + migrations. Domain CRUD lives in a
// `repository.go` inside each owning package (e.g. internal/session/repository.go), Go-idiomatic
// cohesion by bounded context, no separate "data access layer" package.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// dbFilename is fixed; the DataDir is the configurable part.
const dbFilename = "deepcove.duckdb"

// Open returns a *sql.DB backed by a DuckDB file under dataDir. The
// directory is created if missing. The caller owns the returned handle
// and must Close it.
func Open(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("repository: empty data dir")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("repository: mkdir %s: %w", dataDir, err)
	}

	path := filepath.Join(dataDir, dbFilename)
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("repository: open %s: %w", path, err)
	}
	// We deliberately do not call SetMaxOpenConns(1). DuckDB's embedded driver serialises writes
	// itself, so capping the pool only forces concurrent reads to queue up behind each other, which
	// would serialise unrelated HTTP requests once the API layer starts touching the DB. Default pool
	// behaviour is the right one for our access pattern (many short reads, occasional short writes).

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repository: ping %s: %w", path, err)
	}
	return db, nil
}
