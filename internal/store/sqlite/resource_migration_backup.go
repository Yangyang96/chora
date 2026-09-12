package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// A pre-authority migration backup is a consistent snapshot including WAL data.
// Older binaries must restore this snapshot rather than write the upgraded DB.
func backupBeforeResourceMigration(ctx context.Context, db *sql.DB, path string) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version != 33 && version != 34 && version != 35 && version != 36 && version != 37 && version != 41 {
		return nil
	}
	sourceVersion := version
	dir, err := os.MkdirTemp(filepath.Dir(path), fmt.Sprintf(".chora-schema%d-backup-", sourceVersion))
	if err != nil {
		return fmt.Errorf("prepare schema%d backup: %w", sourceVersion, err)
	}
	backup := filepath.Join(dir, "database.sqlite")
	if _, err = db.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		return fmt.Errorf("backup before resource/description migration (migration not started): %w", err)
	}
	if err = os.Chmod(backup, 0600); err != nil {
		return err
	}
	check, err := sql.Open("sqlite", "file:"+backup+"?mode=ro")
	if err != nil {
		return err
	}
	defer check.Close()
	var integrity string
	if err = check.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("schema%d backup integrity failed: %s: %w", sourceVersion, integrity, err)
	}
	if err = check.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != sourceVersion {
		return fmt.Errorf("schema%d backup version mismatch", sourceVersion)
	}
	return nil
}
