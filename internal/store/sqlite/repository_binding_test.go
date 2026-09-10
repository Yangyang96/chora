package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func newRepositoryBindingFor(room domain.Room, locator string, now time.Time) (domain.RepositoryBinding, error) {
	return domain.NewRepositoryBinding(domain.RepositoryBindingParams{
		RoomID: room.ID(), Name: "repo", LocalLocator: locator, SourceKind: domain.RepositorySourceKindOpen,
		AdmittedBase:   "0123456789abcdef0123456789abcdef01234567",
		BaseIdentity:   "sha:0123456789abcdef0123456789abcdef01234567:tree:89abcdef0123456789abcdef0123456789abcdef",
		TargetWorktree: locator, CreatedAt: now, UpdatedAt: now,
	})
}

func openBindingStore(t *testing.T) (*sqlite.Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	db, err := openLatestSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "data", "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, time.Now().UTC().Truncate(time.Microsecond)
}

func insertRoomAndBinding(t *testing.T, db *sqlite.Store, now time.Time, locator string) (domain.Room, domain.RepositoryBinding) {
	t.Helper()
	ctx := context.Background()
	projectID, roomID := domain.NewProjectID(), domain.NewRoomID()
	project, err := domain.NewProject(domain.ProjectParams{ID: projectID, Name: "project", DefaultRoomID: roomID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	room, err := domain.NewRoom(domain.RoomParams{ID: roomID, ProjectID: projectID, OwnershipKind: domain.RoomOwnershipProject, Name: "room", Description: "admitted repo", WorkspaceRoot: locator, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newRepositoryBindingFor(room, locator, now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		return tx.InsertRepositoryBinding(ctx, binding)
	})
	if err != nil {
		t.Fatal(err)
	}
	return room, binding
}

func TestRepositoryBindingRejectsUnclassifiedRoomWithoutRewritingOwnership(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	locator := filepath.Join(t.TempDir(), "unknown")
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "unknown", WorkspaceRoot: locator, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newRepositoryBindingFor(room, locator, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRoom(ctx, room) }); err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRepositoryBinding(ctx, binding) })
	if !errors.Is(err, storecontract.ErrRepositoryBindingConflict) {
		t.Fatalf("InsertRepositoryBinding error=%v", err)
	}
	stored, err := db.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.OwnershipKind() != domain.RoomOwnershipUnclassified || stored.ProjectID().Valid() {
		t.Fatalf("ownership rewritten: %#v", stored)
	}
}

func TestRepositoryBindingRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	locator := filepath.Join(t.TempDir(), "repo")
	room, binding := insertRoomAndBinding(t, db, now, locator)
	loaded, err := db.Reader().GetRepositoryBinding(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !sameBinding(loaded, binding) {
		t.Fatalf("round trip changed binding: %#v != %#v", loaded, binding)
	}
	byLocator, err := db.Reader().GetRepositoryBindingByLocator(ctx, locator)
	if err != nil {
		t.Fatal(err)
	}
	if byLocator.RoomID() != room.ID() {
		t.Fatalf("locator lookup room=%q", byLocator.RoomID())
	}
}

func sameBinding(left, right domain.RepositoryBinding) bool {
	return left.RoomID() == right.RoomID() && left.Name() == right.Name() && left.LocalLocator() == right.LocalLocator() &&
		left.SourceKind() == right.SourceKind() && left.CloneURL() == right.CloneURL() && left.AdmittedBase() == right.AdmittedBase() &&
		left.BaseIdentity() == right.BaseIdentity() && left.TargetWorktree() == right.TargetWorktree() &&
		left.DirtyAdmitted() == right.DirtyAdmitted() && left.State() == right.State() && left.Version() == right.Version() &&
		left.CreatedAt().Equal(right.CreatedAt()) && left.UpdatedAt().Equal(right.UpdatedAt())
}

func TestRepositoryBindingCASConflict(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	locator := filepath.Join(t.TempDir(), "repo")
	room, _ := insertRoomAndBinding(t, db, now, locator)
	current, err := db.Reader().GetRepositoryBinding(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	next, err := current.Remove(current.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRepositoryBindingCAS(ctx, 99, next)
	})
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale CAS error = %v, want ErrVersionConflict", err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRepositoryBindingCAS(ctx, current.Version(), next)
	}); err != nil {
		t.Fatalf("valid CAS error = %v", err)
	}
	removed, err := db.Reader().GetRepositoryBinding(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if removed.State() != domain.RepositoryBindingStateRemoved || removed.Version() != 2 {
		t.Fatalf("removed state=%q version=%d", removed.State(), removed.Version())
	}
}

func TestRepositoryBindingDuplicateRoom(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	locator := filepath.Join(t.TempDir(), "repo")
	room, _ := insertRoomAndBinding(t, db, now, locator)
	duplicate, err := newRepositoryBindingFor(room, filepath.Join(t.TempDir(), "other"), now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertRepositoryBinding(ctx, duplicate)
	})
	if !errors.Is(err, storecontract.ErrRepositoryBindingConflict) {
		t.Fatalf("duplicate room error = %v, want ErrRepositoryBindingConflict", err)
	}
}

func TestRepositoryBindingOverlapByLocator(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	room, _ := insertRoomAndBinding(t, db, now, first)
	if _, err := db.Reader().GetRepositoryBindingByLocator(ctx, second); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("missing locator error = %v, want ErrNotFound", err)
	}
	if _, err := db.Reader().GetRepositoryBindingByLocator(ctx, first); err != nil {
		t.Fatalf("present locator error = %v", err)
	}
	_ = room
}

func TestRepositoryBindingListByState(t *testing.T) {
	ctx := context.Background()
	db, now := openBindingStore(t)
	room, _ := insertRoomAndBinding(t, db, now, filepath.Join(t.TempDir(), "repo"))
	active, err := db.Reader().ListRepositoryBindings(ctx, domain.RepositoryBindingStateActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].RoomID() != room.ID() {
		t.Fatalf("active=%d", len(active))
	}
	removed, err := db.Reader().ListRepositoryBindings(ctx, domain.RepositoryBindingStateRemoved)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed=%d", len(removed))
	}
	if _, err := db.Reader().ListRepositoryBindings(ctx, domain.RepositoryBindingState("bogus")); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid state error = %v, want ErrInvalidArgument", err)
	}
}
