package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkflowV49UpgradeAndUnledgeredSchemaRefusal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chora.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	// Construct the actual predecessor ledger; no populated workflow is deleted.
	if _, err = raw.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range available[:49] {
		if m.version == 19 || m.version == 29 || m.version == 33 {
			if err = applyParentTableRebuildMigration(ctx, raw, m); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err = raw.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err = raw.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, m.version, m.name, m.checksum[:], "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 50 {
		t.Fatalf("upgrade=%d %v", version, err)
	}
	if _, err = raw.Exec(`DELETE FROM schema_migrations WHERE version=50`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, raw); err == nil {
		t.Fatal("unledgered workflow schema accepted")
	}
	if err = raw.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 49 {
		t.Fatalf("failed upgrade advanced=%d %v", version, err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
}
