package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegationMigrationRejectsUnledgeredTables(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, filepath.Join(root, "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Test-owned corruption: retaining new tables while removing their ledger row
	// must fail rather than hiding drift through CREATE IF NOT EXISTS.
	if _, err = db.db.ExecContext(ctx, `DROP TABLE delegation_proposal_sources; DELETE FROM schema_migrations WHERE version>=46`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db.db); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unledgered schema accepted: %v", err)
	}
	var version int
	if err = db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 45 {
		t.Fatalf("failed migration advanced ledger: %d %v", version, err)
	}
}
