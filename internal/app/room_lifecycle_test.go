package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestArchiveRestoreAreCASIdempotentAndReplayExactRoom(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	ctx := context.Background()
	current, err := fixture.db.Reader().GetRoom(ctx, fixture.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	archiveRequest := app.ChangeRoomLifecycleRequest{CommandMeta: meta("archive-room", "archive-room"), RoomID: current.ID(), ExpectedVersion: current.Version()}
	archived, err := fixture.service.ArchiveRoom(ctx, archiveRequest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.service.ArchiveRoom(ctx, archiveRequest)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Room.State() != domain.RoomStateArchived || !replay.Replayed || replay.Room.ID() != archived.Room.ID() || replay.Room.Version() != archived.Room.Version() || !replay.Room.ArchivedAt().Equal(archived.Room.ArchivedAt()) {
		t.Fatalf("archive=%#v replay=%#v", archived, replay)
	}
	reused := archiveRequest
	reused.ExpectedVersion++
	if _, err := fixture.service.ArchiveRoom(ctx, reused); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("changed key reuse err=%v", err)
	}
	staleRestore := app.ChangeRoomLifecycleRequest{CommandMeta: meta("restore-stale", "restore-stale"), RoomID: current.ID(), ExpectedVersion: current.Version()}
	if _, err := fixture.service.RestoreRoom(ctx, staleRestore); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale Restore err=%v", err)
	}
	restoreRequest := app.ChangeRoomLifecycleRequest{CommandMeta: meta("restore-room", "restore-room"), RoomID: current.ID(), ExpectedVersion: archived.Room.Version()}
	restored, err := fixture.service.RestoreRoom(ctx, restoreRequest)
	if err != nil {
		t.Fatal(err)
	}
	restoredReplay, err := fixture.service.RestoreRoom(ctx, restoreRequest)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Room.State() != domain.RoomStateActive || !restored.Room.ArchivedAt().IsZero() || !restoredReplay.Replayed || restoredReplay.Room.Version() != restored.Room.Version() {
		t.Fatalf("restore=%#v replay=%#v", restored, restoredReplay)
	}
	events, err := fixture.db.Reader().ListRoomLifecycleEvents(ctx, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ToState != domain.RoomStateArchived || events[1].ToState != domain.RoomStateActive {
		t.Fatalf("events=%#v", events)
	}
}
