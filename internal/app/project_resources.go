package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var ErrProjectUpgradeRequired = errors.New("upgrade_required: use the task-first API for this Project")
var ErrRepositoryIdentityDrift = errors.New("repository identity changed; original association has been preserved")

// BindTaskResourceIdentity is the application boundary used immediately after
// Task ID allocation. It prevents a client from choosing a Chora-owned branch.
func BindTaskResourceIdentity(snapshot domain.TaskResourceSnapshot, taskID domain.TaskID, taskTitle string) (domain.TaskResourceSnapshot, error) {
	return domain.BindTaskResourceBranches(snapshot, taskID, taskTitle)
}

type CreateEmptyProjectRequest struct {
	CommandMeta
	Name, Description string
}
type ProjectResourcesView struct {
	Project              domain.Project
	Rooms                []domain.Room
	Repositories         []domain.ProjectRepository
	NextRepositoryCursor string
	CurrentAction        *CurrentAction
	CurrentTaskTitle     string
	LastActivityAt       time.Time
	TaskCounts           storecontract.RoomTaskCounts
}

type SaveRepositoryDeliveryDefaultRequest struct {
	CommandMeta
	RepositoryID    domain.RepositoryID
	TargetRef       string
	ExpectedVersion uint64
}

func (s *Service) SaveRepositoryDeliveryDefault(ctx context.Context, req SaveRepositoryDeliveryDefaultRequest) (domain.RepositoryDeliveryDefault, error) {
	if !req.RepositoryID.Valid() || !domain.ValidTaskTargetRef(req.TargetRef) {
		return domain.RepositoryDeliveryDefault{}, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "save_repository_delivery_default", req.RepositoryID.String(), req.ExpectedVersion); err != nil {
		return domain.RepositoryDeliveryDefault{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "save_repository_delivery_default", req.RepositoryID.String(), req.ExpectedVersion, req.TargetRef, now)
	if err != nil {
		return domain.RepositoryDeliveryDefault{}, err
	}
	result := domain.RepositoryDeliveryDefault{RepositoryID: req.RepositoryID, TargetRef: req.TargetRef, Version: req.ExpectedVersion + 1, UpdatedAt: now}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if response, found, lookupErr := tx.LookupCommand(ctx, key); lookupErr != nil {
			return lookupErr
		} else if found {
			var wire struct {
				TargetRef string
				Version   uint64
			}
			if decodeErr := decodeResponse(response, &wire); decodeErr != nil {
				return decodeErr
			}
			result.TargetRef, result.Version = wire.TargetRef, wire.Version
			persisted, getErr := tx.GetRepositoryDeliveryDefault(ctx, req.RepositoryID)
			if getErr != nil || persisted.TargetRef != result.TargetRef || persisted.Version != result.Version {
				return storecontract.ErrVersionConflict
			}
			result = persisted
			return nil
		}
		if err := tx.SaveRepositoryDeliveryDefault(ctx, req.ExpectedVersion, result); err != nil {
			return err
		}
		response, err := jsonResponse(struct {
			TargetRef string
			Version   uint64
		}{result.TargetRef, result.Version})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) CreateEmptyProject(ctx context.Context, req CreateEmptyProjectRequest) (ProjectResourcesView, error) {
	if err := s.requireProjectStore(); err != nil {
		return ProjectResourcesView{}, err
	}
	if !validDisplayName(req.Name) || s.deps.DataRoot == "" {
		return ProjectResourcesView{}, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "create_project", "projects", 0); err != nil {
		return ProjectResourcesView{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "create_project", "projects", 0, struct{ Name, Description string }{req.Name, req.Description}, now)
	if err != nil {
		return ProjectResourcesView{}, err
	}
	var id domain.ProjectID
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire struct{ ID string }
			if err = decodeResponse(response, &wire); err != nil {
				return err
			}
			id, err = domain.ParseProjectID(wire.ID)
			return err
		}
		id = domain.NewProjectID()
		rid := s.deps.IDs.RoomID()
		project, err := domain.NewProject(domain.ProjectParams{ID: id, Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), DefaultRoomID: rid, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		root, err := filepath.Abs(filepath.Join(s.deps.DataRoot, "projects", id.String()))
		if err != nil {
			return err
		}
		description := "Project discussion"
		room, err := domain.NewRoom(domain.RoomParams{ID: rid, ProjectID: id, OwnershipKind: domain.RoomOwnershipProject, Name: "General", Description: description, WorkspaceRoot: root, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err = tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err = tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err = s.insertResourceRoomBrief(ctx, tx, room, req.ActorID, now); err != nil {
			return err
		}
		response, err = jsonResponse(struct{ ID string }{id.String()})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	if err != nil {
		return ProjectResourcesView{}, err
	}
	return s.GetProjectResources(ctx, id)
}
func (s *Service) insertResourceRoomBrief(ctx context.Context, tx storecontract.WriteTx, room domain.Room, actor string, now time.Time) error {
	rev, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "Room Brief", Body: room.Description(), Locator: "room://" + room.ID().String() + "/brief", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}
	confirmed, err := domain.NewRoomRevision(rev, domain.HumanRoomProvenance(actor), now)
	if err != nil {
		return err
	}
	if err = tx.InsertRevision(ctx, rev); err != nil {
		return err
	}
	return tx.InsertRoomRevision(ctx, confirmed)
}
func (s *Service) GetProjectResources(ctx context.Context, id domain.ProjectID) (ProjectResourcesView, error) {
	reader := s.deps.Store.Reader()
	p, err := reader.GetProject(ctx, id)
	if err != nil {
		return ProjectResourcesView{}, err
	}
	rooms, err := reader.ListProjectRooms(ctx, id)
	if err != nil {
		return ProjectResourcesView{}, err
	}
	repos, err := reader.ListProjectRepositories(ctx, id, "", domain.RepositoryMaxPageSize+1)
	if err != nil {
		return ProjectResourcesView{}, err
	}
	out := ProjectResourcesView{Project: p, Rooms: rooms, Repositories: repos, LastActivityAt: p.UpdatedAt()}
	if len(repos) > domain.RepositoryMaxPageSize {
		out.Repositories = repos[:domain.RepositoryMaxPageSize]
		out.NextRepositoryCursor = out.Repositories[len(out.Repositories)-1].Repository.ID.String()
	}
	var actionAt time.Time
	for _, room := range rooms {
		projection, err := reader.GetRoomWorkspace(ctx, room.ID())
		if err != nil {
			return out, err
		}
		out.TaskCounts.Total += projection.Room.TaskCounts.Total
		out.TaskCounts.Open += projection.Room.TaskCounts.Open
		out.TaskCounts.Terminal += projection.Room.TaskCounts.Terminal
		if projection.Room.LastActivityAt.After(out.LastActivityAt) {
			out.LastActivityAt = projection.Room.LastActivityAt
		}
		state := room.State()
		if p.State() == domain.ProjectStateArchived {
			state = domain.RoomStateArchived
		}
		for _, item := range projection.Tasks {
			if item.Task.Archived() {
				continue
			}
			action := deriveCurrentAction(state, item)
			if action.Kind != CurrentActionNone && (out.CurrentAction == nil || item.LastActivityAt.After(actionAt)) {
				out.CurrentAction = &action
				out.CurrentTaskTitle = item.Task.Title()
				actionAt = item.LastActivityAt
			}
		}
	}
	return out, nil
}

type AddProjectRepositoryRequest struct {
	CommandMeta
	ProjectID     domain.ProjectID
	Locator, Name string
}

func (s *Service) AddProjectRepository(ctx context.Context, req AddProjectRepositoryRequest) (domain.ProjectRepository, error) {
	if !req.ProjectID.Valid() || req.Locator == "" || s.deps.RepositorySource == nil {
		return domain.ProjectRepository{}, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "add_project_repository", req.ProjectID.String(), 0); err != nil {
		return domain.ProjectRepository{}, err
	}
	inspection, err := s.deps.RepositorySource.Inspect(ctx, req.Locator)
	if err != nil {
		return domain.ProjectRepository{}, mapInspectError(err)
	}
	if inspection.CommonGitDir == "" || inspection.PhysicalIdentity == "" {
		return domain.ProjectRepository{}, fmt.Errorf("%w: physical repository identity unavailable", ErrRepositoryUnavailable)
	}
	name := req.Name
	if name == "" {
		name = sanitizedRepoBaseName(inspection.CanonicalPath)
	}
	if !validDisplayName(name) {
		return domain.ProjectRepository{}, ErrInvalidCommand
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "add_project_repository", req.ProjectID.String(), 0, struct{ Locator, Name string }{inspection.CanonicalPath, name}, now)
	if err != nil {
		return domain.ProjectRepository{}, err
	}
	var result domain.ProjectRepository
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		project, err := tx.GetProject(ctx, req.ProjectID)
		if err != nil {
			return err
		}
		if project.State() != domain.ProjectStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		if response, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			var wire struct{ RepoID string }
			if err = decodeResponse(response, &wire); err != nil {
				return err
			}
			id, err := domain.ParseRepositoryID(wire.RepoID)
			if err != nil {
				return err
			}
			result, err = tx.GetProjectRepository(ctx, req.ProjectID, id)
			return err
		}
		repo, err := tx.GetRepositoryByCheckout(ctx, inspection.CanonicalPath)
		if errors.Is(err, storecontract.ErrNotFound) {
			repo = domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: name, Checkout: inspection.CanonicalPath, CommonGitDir: inspection.CommonGitDir, PhysicalIdentity: inspection.PhysicalIdentity, IdentitySource: "inspected", CreatedAt: now}
			if err = tx.InsertRepository(ctx, repo); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if repo.IdentitySource == "legacy_unverified" {
			// Legacy admission commit/tree are historical; current HEAD may legitimately advance.
			if repo.LegacyCommit == "" || repo.LegacyTree == "" {
				return fmt.Errorf("%w: legacy identity evidence missing", ErrRepositoryUnavailable)
			}
			if err = gitsource.ProveRevision(ctx, repo.Checkout, repo.LegacyCommit, repo.LegacyTree); err != nil {
				return ErrRepositoryIdentityDrift
			}
			repo.CommonGitDir, repo.PhysicalIdentity, repo.IdentitySource = inspection.CommonGitDir, inspection.PhysicalIdentity, "inspected"
			if err = tx.VerifyLegacyRepository(ctx, repo); err != nil {
				return err
			}
		} else if repo.CommonGitDir != inspection.CommonGitDir || repo.PhysicalIdentity != inspection.PhysicalIdentity {
			return ErrRepositoryIdentityDrift
		}
		a, err := tx.GetProjectRepository(ctx, req.ProjectID, repo.ID)
		expected := uint64(0)
		if err == nil {
			if a.State == "active" {
				result = a
			} else {
				expected = a.Version
				a.State = "active"
				a.Version++
				a.UpdatedAt = now
				result = a
				if err = tx.SaveProjectRepository(ctx, expected, a); err != nil {
					return err
				}
			}
		} else if errors.Is(err, storecontract.ErrNotFound) {
			result = domain.ProjectRepository{ProjectID: req.ProjectID, Repository: repo, State: "active", Version: 1, AddedAt: now, UpdatedAt: now}
			if err = tx.SaveProjectRepository(ctx, 0, result); err != nil {
				return err
			}
		} else {
			return err
		}
		response, err := jsonResponse(struct{ RepoID string }{repo.ID.String()})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

type RemoveProjectRepositoryRequest struct {
	CommandMeta
	ProjectID       domain.ProjectID
	RepositoryID    domain.RepositoryID
	ExpectedVersion uint64
}

func (s *Service) RemoveProjectRepository(ctx context.Context, req RemoveProjectRepositoryRequest) error {
	if req.ExpectedVersion == 0 {
		return ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "remove_project_repository", req.ProjectID.String(), req.ExpectedVersion); err != nil {
		return err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "remove_project_repository", req.ProjectID.String(), req.ExpectedVersion, struct{ RepoID string }{req.RepositoryID.String()}, now)
	if err != nil {
		return err
	}
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			return nil
		}
		p, err := tx.GetProject(ctx, req.ProjectID)
		if err != nil {
			return err
		}
		if p.State() != domain.ProjectStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		pending, err := tx.RepositoryHasPendingWork(ctx, req.RepositoryID)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("%w: repository has active work or pending Apply", storecontract.ErrRoomStateForbidden)
		}
		a, err := tx.GetProjectRepository(ctx, req.ProjectID, req.RepositoryID)
		if err != nil {
			return err
		}
		if a.Version != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		a.State = "removed"
		a.Version++
		a.UpdatedAt = now
		if err = tx.SaveProjectRepository(ctx, req.ExpectedVersion, a); err != nil {
			return err
		}
		response, err := jsonResponse(struct{}{})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
}
func (s *Service) SetRoomRepositoryReferences(ctx context.Context, meta CommandMeta, roomID domain.RoomID, expected uint64, ids []domain.RepositoryID) (domain.RoomRepositoryReferences, error) {
	if err := s.authorize(ctx, meta, "set_room_resources", roomID.String(), expected); err != nil {
		return domain.RoomRepositoryReferences{}, err
	}
	now := s.deps.Clock.Now()
	rawIDs := []string{}
	for _, id := range ids {
		rawIDs = append(rawIDs, id.String())
	}
	key, err := s.commandKey(meta, "set_room_resources", roomID.String(), expected, rawIDs, now)
	if err != nil {
		return domain.RoomRepositoryReferences{}, err
	}
	refs := domain.RoomRepositoryReferences{RoomID: roomID, Version: expected + 1, RepositoryIDs: ids, UpdatedAt: now}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			refs, err = tx.GetRoomRepositoryReferences(ctx, roomID)
			return err
		}
		room, err := tx.GetRoom(ctx, roomID)
		if err != nil {
			return err
		}
		if room.State() != domain.RoomStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		project, err := tx.GetProject(ctx, room.ProjectID())
		if err != nil {
			return err
		}
		if project.State() != domain.ProjectStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		if err = tx.SaveRoomRepositoryReferences(ctx, expected, refs); err != nil {
			return err
		}
		response, err := jsonResponse(struct{}{})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return refs, err
}
