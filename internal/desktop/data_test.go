package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
)

func canonicalTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOwnerLockExcludesSecondOwner(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "data")
	first, err := AcquireOwner(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err = AcquireOwner(root); !errors.Is(err, ErrInUse) {
		t.Fatalf("second lock error=%v", err)
	}
}

func TestBackupRestoreExactRootPreservesNewerData(t *testing.T) {
	base := canonicalTemp(t)
	root := filepath.Join(base, "data")
	backup := filepath.Join(base, "backup")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Backup(root, backup)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DataRoot != root || len(manifest.Entries) != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if err = os.WriteFile(filepath.Join(root, "state"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	recovery, err := Restore(root, backup)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "state"))
	if err != nil || string(got) != "old" {
		t.Fatalf("restored=%q err=%v", got, err)
	}
	newer, err := os.ReadFile(filepath.Join(recovery, "state"))
	if err != nil || string(newer) != "new" {
		t.Fatalf("recovery=%q err=%v", newer, err)
	}
}

func TestRestoreRejectsDifferentAbsoluteRootAndTampering(t *testing.T) {
	base := canonicalTemp(t)
	root := filepath.Join(base, "data")
	backup := filepath.Join(base, "backup")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(root, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(filepath.Join(base, "other"), backup); err == nil {
		t.Fatal("restored backup at a different root")
	}
	if err := os.WriteFile(filepath.Join(backup, "data", "state"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(root, backup); err == nil {
		t.Fatal("restored tampered backup")
	}
}

func TestAdoptBacksUpAndRecordsOriginalRoot(t *testing.T) {
	base := canonicalTemp(t)
	root := filepath.Join(base, "source-data")
	backup := filepath.Join(base, "adoption-backup")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := storesqlite.Open(context.Background(), filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := Adopt(root, backup)
	if err != nil {
		t.Fatal(err)
	}
	if record.DataRoot != root || record.BackupPath != backup {
		t.Fatalf("record=%+v", record)
	}
	if _, err = os.Stat(filepath.Join(root, "desktop-adoption.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(backup, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}
