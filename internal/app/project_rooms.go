package app

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type CreateProjectRoomRequest struct {
	CommandMeta
	ProjectID   domain.ProjectID
	Name        string
	Description string
}

type RenameProjectRequest struct {
	CommandMeta
	ProjectID       domain.ProjectID
	Name            string
	ExpectedVersion uint64
}

type ChangeProjectLifecycleRequest struct {
	CommandMeta
	ProjectID       domain.ProjectID
	ExpectedVersion uint64
}

type RenameRoomRequest struct {
	CommandMeta
	RoomID          domain.RoomID
	Name            string
	Description     string
	ExpectedVersion uint64
}

func validDisplayName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && utf8.RuneCountInString(name) <= 120 && strings.IndexFunc(name, unicode.IsControl) < 0
}

func (s *Service) CreateProjectRoom(ctx context.Context, request CreateProjectRoomRequest) (CreateRoomResult, error) {
	if !request.ProjectID.Valid() || !validDisplayName(request.Name) {
		return CreateRoomResult{}, fmt.Errorf("%w: invalid Project Room request", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "create_project_room", request.ProjectID.String(), 0); err != nil {
		return CreateRoomResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "create_project_room", request.ProjectID.String(), 0, struct{ Name, Description string }{request.Name, request.Description}, now)
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
		project, err := tx.GetProject(ctx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.State() != domain.ProjectStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		defaultRoom, err := tx.GetRoom(ctx, project.DefaultRoomID())
		if err != nil {
			return err
		}
		room, err := domain.NewRoom(domain.RoomParams{ID: s.deps.IDs.RoomID(), ProjectID: project.ID(), OwnershipKind: domain.RoomOwnershipProject, Name: strings.TrimSpace(request.Name), Description: request.Description, WorkspaceRoot: defaultRoom.WorkspaceRoot(), CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err = tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "Room Brief", Body: room.Description(), Locator: "room://" + room.ID().String() + "/brief", CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		initial, err := domain.NewRoomRevision(revision, domain.HumanRoomProvenance(request.ActorID), now)
		if err != nil {
			return err
		}
		if err = tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		if err = tx.InsertRoomRevision(ctx, initial); err != nil {
			return err
		}
		room, err = tx.GetRoom(ctx, room.ID())
		if err != nil {
			return err
		}
		result = CreateRoomResult{Room: room, InitialRevision: initial}
		response, err = roomResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) RenameProject(ctx context.Context, request RenameProjectRequest) (ProjectView, error) {
	if !request.ProjectID.Valid() || request.ExpectedVersion == 0 || !validDisplayName(request.Name) {
		return ProjectView{}, fmt.Errorf("%w: invalid Project rename", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "rename_project", request.ProjectID.String(), request.ExpectedVersion); err != nil {
		return ProjectView{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "rename_project", request.ProjectID.String(), request.ExpectedVersion, struct{ Name string }{request.Name}, now)
	if err != nil {
		return ProjectView{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			return nil
		}
		current, err := tx.GetProject(ctx, request.ProjectID)
		if err != nil {
			return err
		}
		next, err := current.Rename(request.Name, request.ExpectedVersion, now)
		if err != nil {
			return err
		}
		if err = tx.SaveProjectCAS(ctx, request.ExpectedVersion, next); err != nil {
			return err
		}
		response, err := jsonResponse(struct {
			ID string `json:"id"`
		}{next.ID().String()})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	if err != nil {
		return ProjectView{}, err
	}
	return s.GetProjectByID(ctx, request.ProjectID)
}

func (s *Service) ArchiveProject(ctx context.Context, request ChangeProjectLifecycleRequest) (ProjectView, error) {
	return s.ChangeProjectLifecycle(ctx, request, domain.ProjectStateArchived)
}
func (s *Service) RestoreProject(ctx context.Context, request ChangeProjectLifecycleRequest) (ProjectView, error) {
	return s.ChangeProjectLifecycle(ctx, request, domain.ProjectStateActive)
}

func (s *Service) ChangeProjectLifecycle(ctx context.Context, request ChangeProjectLifecycleRequest, target domain.ProjectState) (ProjectView, error) {
	action := "restore_project"
	if target == domain.ProjectStateArchived {
		action = "archive_project"
	} else if target != domain.ProjectStateActive {
		return ProjectView{}, fmt.Errorf("%w: invalid Project lifecycle", ErrInvalidCommand)
	}
	if !request.ProjectID.Valid() || request.ExpectedVersion == 0 {
		return ProjectView{}, fmt.Errorf("%w: invalid Project lifecycle", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.ProjectID.String(), request.ExpectedVersion); err != nil {
		return ProjectView{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, action, request.ProjectID.String(), request.ExpectedVersion, struct{ Target domain.ProjectState }{target}, now)
	if err != nil {
		return ProjectView{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			return nil
		}
		current, err := tx.GetProject(ctx, request.ProjectID)
		if err != nil {
			return err
		}
		var next domain.Project
		if target == domain.ProjectStateArchived {
			next, err = current.Archive(request.ExpectedVersion, now)
		} else {
			next, err = current.Restore(request.ExpectedVersion, now)
		}
		if err != nil {
			return err
		}
		if err = tx.SaveProjectCAS(ctx, request.ExpectedVersion, next); err != nil {
			return err
		}
		response, err := jsonResponse(struct {
			ID string `json:"id"`
		}{next.ID().String()})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	if err != nil {
		return ProjectView{}, err
	}
	return s.GetProjectByID(ctx, request.ProjectID)
}

func (s *Service) RenameRoom(ctx context.Context, request RenameRoomRequest) (domain.Room, error) {
	if !request.RoomID.Valid() || request.ExpectedVersion == 0 || !validDisplayName(request.Name) {
		return domain.Room{}, fmt.Errorf("%w: invalid Room rename", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "rename_room", request.RoomID.String(), request.ExpectedVersion); err != nil {
		return domain.Room{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "rename_room", request.RoomID.String(), request.ExpectedVersion, struct{ Name, Description string }{request.Name, request.Description}, now)
	if err != nil {
		return domain.Room{}, err
	}
	var result domain.Room
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire roomWire
			if err = decodeResponse(response, &wire); err != nil {
				return err
			}
			result, err = restoreRoomWire(wire)
			return err
		}
		current, err := tx.GetRoom(ctx, request.RoomID)
		if err != nil {
			return err
		}
		if current.OwnershipKind() == domain.RoomOwnershipProject {
			project, err := tx.GetProject(ctx, current.ProjectID())
			if err != nil {
				return err
			}
			if project.State() != domain.ProjectStateActive {
				return storecontract.ErrRoomStateForbidden
			}
		}
		if current.State() == domain.RoomStateArchived && current.Description() != request.Description {
			return storecontract.ErrRoomStateForbidden
		}
		result, err = current.Rename(request.Name, request.Description, request.ExpectedVersion, now)
		if err != nil {
			return err
		}
		if err = tx.SaveRoomCAS(ctx, request.ExpectedVersion, result); err != nil {
			return err
		}
		if current.Description() != request.Description {
			revision, revisionErr := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: current.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "Room Brief", Body: request.Description, Locator: "room://" + current.ID().String() + "/brief", CreatedAt: now, UpdatedAt: now})
			if revisionErr != nil {
				return revisionErr
			}
			confirmed, revisionErr := domain.NewRoomRevision(revision, domain.HumanRoomProvenance(request.ActorID), now)
			if revisionErr != nil {
				return revisionErr
			}
			if revisionErr = tx.InsertRevision(ctx, revision); revisionErr != nil {
				return revisionErr
			}
			if revisionErr = tx.InsertRoomRevision(ctx, confirmed); revisionErr != nil {
				return revisionErr
			}
			result, revisionErr = tx.GetRoom(ctx, current.ID())
			if revisionErr != nil {
				return revisionErr
			}
		}
		response, err = jsonResponse(wireRoom(result, ""))
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}
