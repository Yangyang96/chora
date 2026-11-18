package desktop

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestInspectAndAdoptKeepStrictSchemaBoundaries(t *testing.T) {
	for _, mode := range []string{"current", "newer", "checksum", "gap", "wrong-supported"} {
		t.Run(mode, func(t *testing.T) {
			base := canonicalTemp(t)
			root := filepath.Join(base, "data")
			path := filepath.Join(root, "chora.db")
			store, err := storesqlite.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-supported" {
				if _, err = InspectSchema(context.Background(), root, SupportedSchema-1); err == nil {
					t.Fatal("inspector silently accepted another binary version")
				}
				return
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "newer":
				_, err = raw.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,zeroblob(32),'2026-11-17T00:00:00Z')`, SupportedSchema+1, "future.sql")
			case "checksum":
				_, err = raw.Exec(`UPDATE schema_migrations SET checksum=zeroblob(32) WHERE version=?`, SupportedSchema)
			case "gap":
				_, err = raw.Exec(`DELETE FROM schema_migrations WHERE version=?`, SupportedSchema-1)
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = raw.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := InspectSchema(context.Background(), root, SupportedSchema)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "current" {
				if inspection.Compatibility != "current" {
					t.Fatal(inspection)
				}
				if _, err = Adopt(root, filepath.Join(base, "backup")); err != nil {
					t.Fatal(err)
				}
			} else {
				if inspection.Compatibility == "current" || inspection.Compatibility == "upgrade_required" {
					t.Fatalf("unsafe ledger accepted: %+v", inspection)
				}
				if _, err = Adopt(root, filepath.Join(base, "backup")); err == nil {
					t.Fatal("unsafe adoption accepted")
				}
				after, err := os.ReadFile(path)
				if err != nil || string(before) != string(after) {
					t.Fatal("inspection/adoption changed rejected database", err)
				}
				if _, err = os.Stat(filepath.Join(root, "desktop-adoption.json")); !os.IsNotExist(err) {
					t.Fatal("rejected source gained adoption authority")
				}
			}
		})
	}
}
