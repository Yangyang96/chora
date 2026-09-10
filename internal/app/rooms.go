package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type roomWire struct {
	ID, Name, Description, WorkspaceRoot, RevisionID string
	ProjectID                                        string
	OwnershipKind                                    domain.RoomOwnershipKind
	State                                            domain.RoomState
	Version                                          uint64
	CreatedAt, UpdatedAt, ArchivedAt                 time.Time
}

func roomResponse(result CreateRoomResult) (storecontract.Response, error) {
	return jsonResponse(wireRoom(result.Room, result.InitialRevision.ID().String()))
}
func wireRoom(room domain.Room, revisionID string) roomWire {
	return roomWire{ID: room.ID().String(), ProjectID: room.ProjectID().String(), OwnershipKind: room.OwnershipKind(), Name: room.Name(), Description: room.Description(), WorkspaceRoot: room.WorkspaceRoot(), RevisionID: revisionID, State: room.State(), Version: room.Version(), CreatedAt: room.CreatedAt(), UpdatedAt: room.UpdatedAt(), ArchivedAt: room.ArchivedAt()}
}
func restoreRoomWire(w roomWire) (domain.Room, error) {
	id, err := domain.ParseRoomID(w.ID)
	if err != nil {
		return domain.Room{}, err
	}
	var projectID domain.ProjectID
	if w.ProjectID != "" {
		projectID, err = domain.ParseProjectID(w.ProjectID)
		if err != nil {
			return domain.Room{}, err
		}
	}
	return domain.RestoreRoom(domain.RoomRecord{ID: id, ProjectID: projectID, OwnershipKind: w.OwnershipKind, Name: w.Name, Description: w.Description, WorkspaceRoot: w.WorkspaceRoot, State: w.State, Version: w.Version, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, ArchivedAt: w.ArchivedAt})
}
func replayRoom(ctx context.Context, response storecontract.Response, reader storecontract.Reader) (CreateRoomResult, error) {
	var w roomWire
	if err := json.Unmarshal(response.Body, &w); err != nil {
		return CreateRoomResult{}, err
	}
	room, err := restoreRoomWire(w)
	if err != nil {
		return CreateRoomResult{}, err
	}
	revisionID, err := domain.ParseContextRevisionID(w.RevisionID)
	if err != nil {
		return CreateRoomResult{}, err
	}
	revision, err := reader.GetRoomRevision(ctx, revisionID)
	return CreateRoomResult{Room: room, InitialRevision: revision, Replayed: true}, err
}
func (s *Service) CreateRoom(ctx context.Context, request CreateRoomRequest) (CreateRoomResult, error) {
	if s.deps.RoomCreationMode == RoomCreationProjectOnly {
		return CreateRoomResult{}, fmt.Errorf("%w: standalone Room creation is disabled", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "create_room", "rooms", 0); err != nil {
		return CreateRoomResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "create_room", "rooms", 0, struct{ Name, Description, WorkspaceRoot string }{request.Name, request.Description, request.WorkspaceRoot}, now)
	if err != nil {
		return CreateRoomResult{}, err
	}
	var result CreateRoomResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			result, err = replayRoom(ctx, response, tx)
			return err
		}
		room, err := domain.NewRoom(domain.RoomParams{ID: s.deps.IDs.RoomID(), OwnershipKind: domain.RoomOwnershipLegacyStandalone, Name: request.Name, Description: request.Description, WorkspaceRoot: request.WorkspaceRoot, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
			EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief,
			RevisionNumber: 1, Title: "Room Brief", Body: room.Description(), Locator: "room://" + room.ID().String() + "/brief", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		initialRevision, err := domain.NewRoomRevision(contextRevision, domain.HumanRoomProvenance(request.ActorID), now)
		if err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, contextRevision); err != nil {
			return err
		}
		if err := tx.InsertRoomRevision(ctx, initialRevision); err != nil {
			return err
		}
		room, err = tx.GetRoom(ctx, room.ID())
		if err != nil {
			return err
		}
		result = CreateRoomResult{Room: room, InitialRevision: initialRevision}
		response, err = roomResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) ArchiveRoom(ctx context.Context, request ChangeRoomLifecycleRequest) (ChangeRoomLifecycleResult, error) {
	return s.changeRoomLifecycle(ctx, request, domain.RoomStateArchived)
}

func (s *Service) RestoreRoom(ctx context.Context, request ChangeRoomLifecycleRequest) (ChangeRoomLifecycleResult, error) {
	return s.changeRoomLifecycle(ctx, request, domain.RoomStateActive)
}

func (s *Service) changeRoomLifecycle(ctx context.Context, request ChangeRoomLifecycleRequest, target domain.RoomState) (ChangeRoomLifecycleResult, error) {
	var action string
	switch target {
	case domain.RoomStateArchived:
		action = "archive_room"
	case domain.RoomStateActive:
		action = "restore_room"
	default:
		return ChangeRoomLifecycleResult{}, fmt.Errorf("%w: invalid Room lifecycle target", ErrInvalidCommand)
	}
	if request.ExpectedVersion == 0 || !request.RoomID.Valid() {
		return ChangeRoomLifecycleResult{}, fmt.Errorf("%w: invalid Room lifecycle request", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.RoomID.String(), request.ExpectedVersion); err != nil {
		return ChangeRoomLifecycleResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, action, request.RoomID.String(), request.ExpectedVersion, struct {
		Target domain.RoomState
	}{Target: target}, now)
	if err != nil {
		return ChangeRoomLifecycleResult{}, err
	}
	var result ChangeRoomLifecycleResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire roomWire
			if err := decodeResponse(response, &wire); err != nil {
				return err
			}
			result.Room, err = restoreRoomWire(wire)
			result.Replayed = err == nil
			return err
		}
		current, err := tx.GetRoom(ctx, request.RoomID)
		if err != nil {
			return err
		}
		if current.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		var next domain.Room
		if target == domain.RoomStateArchived {
			next, err = current.Archive(request.ExpectedVersion, now)
		} else {
			next, err = current.Restore(request.ExpectedVersion, now)
		}
		if err != nil {
			return err
		}
		event := storecontract.RoomLifecycleEvent{
			RoomID: next.ID(), FromState: current.State(), ToState: next.State(), Version: next.Version(),
			ActorID: request.ActorID, SessionID: request.SessionID, IdempotencyKeyHash: key.KeyHash,
			RequestDigest: key.RequestDigest, OccurredAt: now,
		}
		if err := tx.SaveRoomLifecycleCAS(ctx, request.ExpectedVersion, next, event); err != nil {
			return err
		}
		result.Room = next
		response, err = jsonResponse(wireRoom(next, ""))
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}
