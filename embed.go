// Package deepcove is the module root, used solely to host static assets
// embedded via //go:embed. The directive can only reach files within the
// embedding file's directory tree, so this file must live at module root
// to see both ui/build/ and db/migrations/.
package deepcove

import "embed"

// `all:` includes files whose names start with `.` or `_` — required
// because SvelteKit emits assets under `_app/`.
//
//go:embed all:ui/build
var assets embed.FS

//go:embed db/migrations/*.sql
var migrations embed.FS

// Assets returns the embedded SvelteKit build output. Paths are rooted at
// the module (e.g., "ui/build/index.html"). Most callers should use the
// convenience helper in internal/ui, which sub-roots at "ui/build/".
func Assets() embed.FS {
	return assets
}

// Migrations returns the embedded SQL migration files. Paths are rooted at
// the module (e.g., "db/migrations/0001_schema.sql"). internal/db walks
// these in sorted order.
func Migrations() embed.FS {
	return migrations
}
