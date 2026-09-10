package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestOpenRejectsSymlinkedDataDirectoryComponent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if db, err := sqlite.Open(context.Background(), filepath.Join(link, "chora.db")); err == nil {
		db.Close()
		t.Fatal("symlinked data directory accepted")
	}
}

func TestOpenRejectsSymlinkedNonDirectAncestorBelowTrustedTempRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	child := filepath.Join(real, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "child", "data", "chora.db")
	if db, err := sqlite.Open(context.Background(), path); err == nil {
		db.Close()
		t.Fatal("symlinked non-direct ancestor accepted")
	}
}

func TestOpenRejectsSymlinkedDatabase(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "chora.db")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if db, err := sqlite.Open(context.Background(), path); err == nil {
		db.Close()
		t.Fatal("symlinked database accepted")
	}
}

func TestOpenRejectsInsecureExistingDataDirectoryWithoutChmod(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0o750); err != nil {
		t.Fatal(err)
	}
	before := fileMode(t, data).Perm()
	if db, err := sqlite.Open(context.Background(), filepath.Join(data, "chora.db")); err == nil {
		db.Close()
		t.Fatal("insecure existing data directory accepted")
	}
	if after := fileMode(t, data).Perm(); after != before {
		t.Fatalf("existing data directory chmodded from %#o to %#o", before, after)
	}
}

func TestOpenRejectsInsecureExistingDatabaseWithoutChmod(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "chora.db")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	before := fileMode(t, path).Perm()
	if db, err := sqlite.Open(context.Background(), path); err == nil {
		db.Close()
		t.Fatal("insecure existing database accepted")
	}
	if after := fileMode(t, path).Perm(); after != before {
		t.Fatalf("existing database chmodded from %#o to %#o", before, after)
	}
}

func TestOpenCreatesOnlyImmediateDataDirectoryAndDatabaseSecurely(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	path := filepath.Join(data, "chora.db")
	db, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := fileMode(t, data).Perm(); got != 0o700 {
		t.Fatalf("data dir mode=%#o", got)
	}
	if got := fileMode(t, path).Perm(); got != 0o600 {
		t.Fatalf("db mode=%#o", got)
	}
}
