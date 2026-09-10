package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestRepositoryDeliveryDefaultPersistsAndUsesCAS(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "delivery-default.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	repository := domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: "repo", Checkout: filepath.Join(t.TempDir(), "repo"), CommonGitDir: filepath.Join(t.TempDir(), "git"), PhysicalIdentity: strings.Repeat("a", 64), IdentitySource: "inspected", CreatedAt: now}
	if err = store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRepository(ctx, repository); err != nil {
			return err
		}
		return tx.SaveRepositoryDeliveryDefault(ctx, 0, domain.RepositoryDeliveryDefault{RepositoryID: repository.ID, TargetRef: "refs/heads/main", Version: 1, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRepositoryDeliveryDefault(ctx, 0, domain.RepositoryDeliveryDefault{RepositoryID: repository.ID, TargetRef: "refs/heads/release", Version: 1, UpdatedAt: now})
	}); err == nil {
		t.Fatal("stale default update succeeded")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Reader().GetRepositoryDeliveryDefault(ctx, repository.ID)
	if err != nil || got.TargetRef != "refs/heads/main" || got.Version != 1 {
		t.Fatalf("default=%+v err=%v", got, err)
	}
}
