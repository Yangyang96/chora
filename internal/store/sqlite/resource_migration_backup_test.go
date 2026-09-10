package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestResourceMigrationBacksUpSchema33AndPreservesBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA foreign_keys=ON;CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:33] {
		if m.version == 19 || m.version == 29 || m.version == 33 {
			err = applyParentTableRebuildMigration(ctx, db, m)
		} else {
			err = applyMigration(ctx, db, m)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// A legacy BLOB fixture must remain the same storage class and exact bytes.
	if _, err = db.Exec(`CREATE TABLE preservation_fixture(id TEXT PRIMARY KEY,payload BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	raw := []byte("old intent\x00unchanged\xff")
	if _, err = db.Exec(`INSERT INTO preservation_fixture VALUES('old',?)`, raw); err != nil {
		t.Fatal(err)
	}
	if err = backupBeforeResourceMigration(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".chora-schema33-backup-*", "database.sqlite"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup=%v err=%v", backups, err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, backups[0]} {
		copyDB, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		var kind string
		if err = copyDB.QueryRow(`SELECT payload,typeof(payload) FROM preservation_fixture WHERE id='old'`).Scan(&got, &kind); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, got) || kind != "blob" {
			t.Fatal("legacy bytes changed")
		}
		var version int
		if err = copyDB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		expected := len(migrations)
		if file == backups[0] {
			expected = 33
		}
		if version != expected {
			t.Fatalf("version=%d want%d", version, expected)
		}
		copyDB.Close()
	}
	// Backup is a usable old-schema database, including all historical tables.
	restored, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.Exec(`INSERT INTO preservation_fixture VALUES('restored',?)`, []byte(time.Now().String())); err != nil {
		t.Fatal(err)
	}
}

func TestTaskDeliveryMigrationBacksUpSchema36BeforeAddingAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range available[:36] {
		if m.version == 19 || m.version == 29 || m.version == 33 {
			err = applyParentTableRebuildMigration(ctx, db, m)
		} else {
			err = applyMigration(ctx, db, m)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`CREATE TABLE preservation_fixture(payload BLOB NOT NULL); INSERT INTO preservation_fixture VALUES(x'ff00616263')`); err != nil {
		t.Fatal(err)
	}
	if err = backupBeforeResourceMigration(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".chora-schema36-backup-*", "database.sqlite"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup=%v %v", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	for _, source := range []*sql.DB{db, backup} {
		var payload []byte
		if err = source.QueryRow(`SELECT payload FROM preservation_fixture`).Scan(&payload); err != nil || !bytes.Equal(payload, []byte{255, 0, 'a', 'b', 'c'}) {
			t.Fatalf("old bytes changed %x %v", payload, err)
		}
		var integrity string
		if err = source.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity=%s %v", integrity, err)
		}
	}
	var version int
	if err = backup.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 36 {
		t.Fatalf("backup schema=%d %v", version, err)
	}
}
