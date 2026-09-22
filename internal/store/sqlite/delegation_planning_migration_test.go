package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanningMigrationRejectsUnledgeredTable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, filepath.Join(root, "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=48`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db.db); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unledgered planning table accepted: %v", err)
	}
	var version int
	if err = db.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 47 {
		t.Fatalf("migration advanced: %d %v", version, err)
	}
}
