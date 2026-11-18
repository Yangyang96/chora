package migrations

import "embed"

// CurrentVersion is the supported SQLite format of this build. Every offline
// inspector and adoption entry point uses the same version as the migrator.
// The embedded migration tests enforce that this matches the complete ledger.
const CurrentVersion = 51

// Files contains the immutable, forward-only SQLite migrations.
//
//go:embed *.sql
var Files embed.FS
