package migrations

import "embed"

// Files contains the immutable, forward-only SQLite migrations.
//
//go:embed *.sql
var Files embed.FS
