package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegationProposalMigrationRejectsUnledgeredTable(t *testing.T) {
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
	if _, err = db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=47`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db.db); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unledgered proposal table accepted: %v", err)
	}
	var version int
	if err = db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 46 {
		t.Fatalf("migration ledger advanced: %d %v", version, err)
	}
}
