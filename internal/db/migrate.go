package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/waltzofpearls/deepcove"
)

// migrationsDir is the path inside the embed.FS that holds the .sql
// files. Keep in sync with the //go:embed directive in embed.go.
const migrationsDir = "db/migrations"

// ErrOutOfOrderMigration is returned when a previously-unseen migration
// file would land lexically before a migration that has already been
// applied. Applying it now would yield a schema state inconsistent
// with a fresh run from scratch, so we refuse and let the operator
// resolve it.
var ErrOutOfOrderMigration = errors.New("migrate: out-of-order migration detected")

// Migrate applies any embedded migrations that haven't been recorded in the schema_migrations table.
// Migrations run in lexical order of their filenames; the 4-digit prefix (0001_, 0002_, ...) keeps
// ordering stable. Each file runs in its own transaction.
//
// We refuse to apply migrations out of order: if a new file would
// land lexically before something already applied, we error rather
// than produce a schema state that a fresh deploy could never reach.
// This catches the common "merged a branch with a new migration
// numbered before someone else's" mistake.
func Migrate(db *sql.DB) error {
	return migrate(db, deepcove.Migrations(), migrationsDir)
}

// migrate is the underlying implementation, parameterised on the
// migration filesystem so unit tests can drive it with fstest.MapFS.
func migrate(db *sql.DB, embedFS fs.FS, dir string) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied, err := loadApplied(db)
	if err != nil {
		return err
	}

	files, err := listMigrations(embedFS, dir)
	if err != nil {
		return err
	}

	// Highest already-applied name (lexically). Any unapplied file
	// sorting before this is the "gap" case we refuse.
	var maxApplied string
	for v := range applied {
		if v > maxApplied {
			maxApplied = v
		}
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		if maxApplied != "" && name < maxApplied {
			return fmt.Errorf(
				"%w: %s would apply after %s is already applied; resolve by reordering, renaming, or wiping the DB",
				ErrOutOfOrderMigration, name, maxApplied,
			)
		}
		sqlBytes, err := fs.ReadFile(embedFS, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := applyOne(db, name, string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

func loadApplied(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: select applied: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scan applied: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

func listMigrations(embedFS fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(embedFS, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read embedded migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func applyOne(db *sql.DB, name, body string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migrate: begin %s: %w", name, err)
	}
	// Each migration file may contain multiple SQL statements separated by semicolons. We rely on
	// go-duckdb's Exec accepting a multi-statement string and running them sequentially within the
	// same transaction. This is driver behaviour, not a standard database/sql guarantee; if we ever
	// switch drivers we will need a SQL-aware splitter here.
	if _, err := tx.Exec(body); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migrate: exec %s: %w", name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migrate: record %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit %s: %w", name, err)
	}
	return nil
}
