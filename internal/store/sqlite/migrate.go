package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/migrations"
)

type migration struct {
	version  int
	name     string
	checksum [32]byte
	sql      string
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	available, err := loadMigrations()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	type applied struct {
		version  int
		name     string
		checksum []byte
	}
	var existing []applied
	for rows.Next() {
		var a applied
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i, a := range existing {
		if a.version != i+1 {
			return fmt.Errorf("migration gap: expected version %d, found %d", i+1, a.version)
		}
		if a.version > len(available) {
			return fmt.Errorf("database schema version %d is newer than supported %d", a.version, len(available))
		}
		want := available[a.version-1]
		if a.name != want.name || len(a.checksum) != 32 || string(a.checksum) != string(want.checksum[:]) {
			return fmt.Errorf("migration checksum drift at version %d", a.version)
		}
	}
	for _, m := range available[len(existing):] {
		if m.version == 15 {
			var legacyCount int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM task_plans`).Scan(&legacyCount); err != nil {
				return fmt.Errorf("inspect legacy task planning data: %w", err)
			}
			if legacyCount != 0 {
				return fmt.Errorf("%w: found %d mutable task_plans row(s); start Chora with a new data directory", storecontract.ErrLegacyPlanningData, legacyCount)
			}
		}
		if m.version == 19 || m.version == 29 || m.version == 33 {
			if err := applyParentTableRebuildMigration(ctx, db, m); err != nil {
				return err
			}
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, m.version, m.name, m.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.name, err)
	}
	return nil
}

// SQLite cannot rebuild a referenced parent table while foreign-key enforcement
// is enabled. The setting is connection-local and cannot be changed inside a
// transaction, so parent rebuilds own one connection, disable enforcement before BEGIN,
// validates the rebuilt graph inside the transaction, and restores enforcement
// before returning the connection to the pool.
func applyParentTableRebuildMigration(ctx context.Context, db *sql.DB, m migration) (resultErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration %s: %w", m.name, err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("restore foreign keys after migration %s: %w", m.name, err)
		}
		var enabled int
		if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("verify foreign keys after migration %s: %w", m.name, err)
		} else if err == nil && enabled != 1 && resultErr == nil {
			resultErr = fmt.Errorf("foreign keys remain disabled after migration %s", m.name)
		}
	}()
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 0 {
		return fmt.Errorf("foreign keys remain enabled for parent-table migration %s", m.name)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	var roomReferenceViolation int
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM tasks child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM context_entries child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM candidates child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM room_revision_records child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM task_revision_selections child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM task_revision_exclusions child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)
		OR EXISTS(SELECT 1 FROM room_lifecycle_events child LEFT JOIN rooms parent ON parent.id=child.room_id WHERE parent.id IS NULL)`).Scan(&roomReferenceViolation)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("check Room references for migration %s: %w", m.name, err)
	}
	if roomReferenceViolation != 0 {
		_ = tx.Rollback()
		return fmt.Errorf("migration %s leaves a Room foreign-key violation", m.name)
	}
	if m.version == 29 {
		// Check the rebuilt review graph, preserving the historical legacy-data
		// tolerance outside this migration's ownership (as v19 does for Rooms).
		for _, table := range []string{"patch_applications", "patch_inspection_failures", "verified_review_routes", "local_review_results", "local_review_decisions"} {
			rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
			if err != nil {
				_ = tx.Rollback()
				return err
			}
			violation := rows.Next()
			checkErr := rows.Err()
			_ = rows.Close()
			if violation || checkErr != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s leaves an invalid %s foreign-key graph: %v", m.name, table, checkErr)
			}
		}
	}
	if m.version == 33 {
		for _, table := range []string{"runs", "attempts", "agent_reports", "run_events"} {
			rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
			if err != nil {
				_ = tx.Rollback()
				return err
			}
			violation := rows.Next()
			checkErr := rows.Err()
			_ = rows.Close()
			if violation || checkErr != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s leaves an invalid %s foreign-key graph: %v", m.name, table, checkErr)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, m.version, m.name, m.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.name, err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var result []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("invalid migration name %s", entry.Name())
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, migration{version: version, name: entry.Name(), checksum: sha256.Sum256(body), sql: string(body)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for i, m := range result {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration source gap at %d", i+1)
		}
	}
	return result, nil
}
