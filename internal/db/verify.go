package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ErrIndexLostRows means a row the table holds cannot be found through an index. DuckDB can persist an
// index that lost rows when a process exits without closing the database, and then answers queries with
// nothing instead of failing (duckdb#23848).
var ErrIndexLostRows = fmt.Errorf("db: an index cannot see rows the table still holds")

// ErrNotSetUp means this database has no schema yet, which is what an untouched data dir looks like.
var ErrNotSetUp = fmt.Errorf("db: this data dir has no database yet")

// SetUp reports whether the schema is there. A command that only reads has no business running the
// migrations itself, so it asks first and says what it found.
func SetUp(ctx context.Context, database *sql.DB) (bool, error) {
	var tables int
	err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM duckdb_tables() WHERE table_name = 'sessions'`).Scan(&tables)
	if err != nil {
		return false, fmt.Errorf("db: look for the schema: %w", err)
	}
	return tables > 0, nil
}

// VerifyIndexes looks up every session by name, which is the one indexed column left, and compares that
// with what a plain scan finds. Cheap: this table holds tens of rows.
func VerifyIndexes(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `SELECT id, name FROM sessions`)
	if err != nil {
		return fmt.Errorf("db: read sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	names := make(map[string]string)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("db: read sessions: %w", err)
		}
		names[name] = id
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: read sessions: %w", err)
	}

	for name, id := range names {
		var found int
		if err := database.QueryRowContext(ctx,
			`SELECT count(*) FROM sessions WHERE name = ?`, name).Scan(&found); err != nil {
			return fmt.Errorf("db: look up session %s: %w", id, err)
		}
		if found == 0 {
			return fmt.Errorf("%w: session %s is invisible to a lookup by name", ErrIndexLostRows, id)
		}
	}
	return nil
}

// RebuildIndexes drops and recreates every index DuckDB reports, which is how a lost index is repaired.
// An index behind a UNIQUE constraint cannot be dropped, so it is left alone and reported by the caller.
func RebuildIndexes(ctx context.Context, database *sql.DB) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT index_name, sql FROM duckdb_indexes()`)
	if err != nil {
		return nil, fmt.Errorf("db: list indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type index struct{ name, create string }
	var found []index
	for rows.Next() {
		var ix index
		var create sql.NullString
		if err := rows.Scan(&ix.name, &create); err != nil {
			return nil, fmt.Errorf("db: list indexes: %w", err)
		}
		ix.create = create.String
		found = append(found, ix)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list indexes: %w", err)
	}

	var rebuilt []string
	for _, ix := range found {
		if ix.create == "" {
			continue
		}
		if _, err := database.ExecContext(ctx, `DROP INDEX IF EXISTS `+ix.name); err != nil {
			return rebuilt, fmt.Errorf("db: drop index %s: %w", ix.name, err)
		}
		if _, err := database.ExecContext(ctx, ix.create); err != nil {
			return rebuilt, fmt.Errorf("db: recreate index %s: %w", ix.name, err)
		}
		rebuilt = append(rebuilt, ix.name)
	}
	return rebuilt, nil
}
