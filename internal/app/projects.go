package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const projectRoomDescription = "Admitted local Git repository"

type ProjectView struct {
	Project           domain.Project
	Rooms             []domain.Room
	CurrentAction     *CurrentAction
	CurrentTaskTitle  string
	Room              domain.Room
	RepositoryBinding domain.RepositoryBinding
	LastActivityAt    time.Time
	TaskCounts        storecontract.RoomTaskCounts
}

type OpenProjectRequest struct {
	CommandMeta
	Locator string
	Name    string
}

type OpenProjectResult struct {
	Project  ProjectView
	Replayed bool
}

type CloneProjectRequest struct {
	CommandMeta
	URL string
}

type CloneProjectResult struct {
	Project  ProjectView
	Replayed bool
}

type RemoveProjectRequest struct {
	CommandMeta
	RoomID          domain.RoomID
	ExpectedVersion uint64
}

type RemoveProjectResult struct {
	Room     domain.Room
	Replayed bool
}

func (s *Service) OpenProject(ctx context.Context, request OpenProjectRequest) (OpenProjectResult, error) {
	if err := s.requireProjectStore(); err != nil {
		return OpenProjectResult{}, err
	}
	if s.deps.RepositorySource == nil {
		return OpenProjectResult{}, ErrRepositoryUnavailable
	}
	if err := s.authorize(ctx, request.CommandMeta, "open_project", "projects", 0); err != nil {
		return OpenProjectResult{}, err
	}
	if strings.TrimSpace(request.Locator) == "" {
		return OpenProjectResult{}, fmt.Errorf("%w: locator is required", ErrInvalidCommand)
	}
	inspection, err := s.deps.RepositorySource.Inspect(ctx, request.Locator)
	if err != nil {
		return OpenProjectResult{}, mapInspectError(err)
	}
	name := sanitizedRepoBaseName(inspection.CanonicalPath)
	if request.Name != "" {
		name = strings.TrimSpace(request.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return OpenProjectResult{}, fmt.Errorf("%w: project name must contain 1–120 characters without control characters", ErrInvalidCommand)
		}
	}
	project, replayed, err := s.admitInspection(ctx, request.CommandMeta, "open_project", projectBindingParams{
		RoomID:         s.deps.IDs.RoomID(),
		Name:           name,
		LocalLocator:   inspection.CanonicalPath,
		SourceKind:     domain.RepositorySourceKindOpen,
		AdmittedBase:   inspection.HeadCommit,
		BaseIdentity:   baseIdentityFor(inspection.HeadCommit, inspection.RootTree),
		TargetWorktree: inspection.CanonicalPath,
		DirtyAdmitted:  inspection.Dirty,
	})
	if err != nil {
		return OpenProjectResult{}, err
	}
	return OpenProjectResult{Project: project, Replayed: replayed}, nil
}

func (s *Service) CloneProject(ctx context.Context, request CloneProjectRequest) (CloneProjectResult, error) {
	if err := s.requireProjectStore(); err != nil {
		return CloneProjectResult{}, err
	}
	if s.deps.RepositorySource == nil {
		return CloneProjectResult{}, ErrRepositoryUnavailable
	}
	if err := s.authorize(ctx, request.CommandMeta, "clone_project", "projects", 0); err != nil {
		return CloneProjectResult{}, err
	}
	if strings.TrimSpace(request.URL) == "" {
		return CloneProjectResult{}, fmt.Errorf("%w: clone URL is required", ErrInvalidCommand)
	}
	if strings.TrimSpace(s.deps.DataRoot) == "" {
		return CloneProjectResult{}, fmt.Errorf("%w: data root is unavailable", ErrInvalidCommand)
	}
	projectsDir := filepath.Join(s.deps.DataRoot, "projects")
	if err := os.MkdirAll(projectsDir, 0o700); err != nil {
		return CloneProjectResult{}, fmt.Errorf("create project destination: %w", err)
	}
	resolvedProjectsDir, err := filepath.EvalSymlinks(projectsDir)
	if err != nil {
		return CloneProjectResult{}, fmt.Errorf("resolve project destination: %w", err)
	}
	dest := filepath.Join(resolvedProjectsDir, sanitizeURLBasename(request.URL))
	if existing, err := s.deps.Store.Reader().GetRepositoryBindingByLocator(ctx, dest); err == nil {
		if existing.SourceKind() != domain.RepositorySourceKindClone || existing.CloneURL() != request.URL {
			return CloneProjectResult{}, ErrProjectOverlap
		}
		project, err := s.GetProject(ctx, existing.RoomID())
		if err != nil {
			return CloneProjectResult{}, err
		}
		return CloneProjectResult{Project: project, Replayed: true}, nil
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return CloneProjectResult{}, err
	}
	inspection, err := s.deps.RepositorySource.Clone(ctx, request.URL, dest)
	if err != nil {
		return CloneProjectResult{}, ErrCloneFailed
	}
	project, replayed, err := s.admitInspection(ctx, request.CommandMeta, "clone_project", projectBindingParams{
		RoomID:         s.deps.IDs.RoomID(),
		Name:           sanitizedRepoBaseName(inspection.CanonicalPath),
		LocalLocator:   inspection.CanonicalPath,
		SourceKind:     domain.RepositorySourceKindClone,
		CloneURL:       request.URL,
		AdmittedBase:   inspection.HeadCommit,
		BaseIdentity:   baseIdentityFor(inspection.HeadCommit, inspection.RootTree),
		TargetWorktree: inspection.CanonicalPath,
		DirtyAdmitted:  inspection.Dirty,
	})
	if err != nil {
		return CloneProjectResult{}, err
	}
	return CloneProjectResult{Project: project, Replayed: replayed}, nil
}

type projectBindingParams struct {
	RoomID         domain.RoomID
	Name           string
	LocalLocator   string
	SourceKind     domain.RepositorySourceKind
	CloneURL       string
	AdmittedBase   string
	BaseIdentity   string
	TargetWorktree string
	DirtyAdmitted  bool
}

// admitInspection performs the idempotent-reopen, overlap guard, and atomic
// Room + brief + binding creation shared by OpenProject and CloneProject.
func (s *Service) admitInspection(ctx context.Context, meta CommandMeta, action string, params projectBindingParams) (ProjectView, bool, error) {
	if existing, err := s.deps.Store.Reader().GetRepositoryBindingByLocator(ctx, params.LocalLocator); err == nil {
		project, err := s.GetProject(ctx, existing.RoomID())
		if err != nil {
			return ProjectView{}, false, err
		}
		return project, true, nil
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return ProjectView{}, false, err
	}
	if err := s.rejectOverlap(ctx, params.LocalLocator); err != nil {
		return ProjectView{}, false, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(meta, action, "projects", 0, struct{ Locator string }{params.LocalLocator}, now)
	if err != nil {
		return ProjectView{}, false, err
	}
	var project ProjectView
	var replayed bool
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire projectWire
			if err := decodeResponse(response, &wire); err != nil {
				return err
			}
			project, err = restoreProjectWire(wire)
			if err != nil {
				return err
			}
			replayed = true
			return nil
		}
		// Re-check containment inside the write transaction to close the
		// concurrent-admit window; the outer rejectOverlap is a fast path.
		bindings, err := listAllRepositoryBindings(ctx, tx)
		if err != nil {
			return err
		}
		for _, other := range bindings {
			if pathContains(other.LocalLocator(), params.LocalLocator) || pathContains(params.LocalLocator, other.LocalLocator()) {
				return ErrProjectOverlap
			}
		}
		binding, err := domain.NewRepositoryBinding(domain.RepositoryBindingParams{
			RoomID: params.RoomID, Name: params.Name, LocalLocator: params.LocalLocator, SourceKind: params.SourceKind,
			CloneURL: params.CloneURL, AdmittedBase: params.AdmittedBase, BaseIdentity: params.BaseIdentity,
			TargetWorktree: params.TargetWorktree, DirtyAdmitted: params.DirtyAdmitted, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		project, err = s.insertProject(ctx, tx, meta.ActorID, binding, now)
		if err != nil {
			return err
		}
		response, err = jsonResponse(wireProject(project))
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return project, replayed, err
}

func (s *Service) insertProject(ctx context.Context, tx storecontract.WriteTx, actorID string, binding domain.RepositoryBinding, now time.Time) (ProjectView, error) {
	project, err := domain.NewProject(domain.ProjectParams{ID: domain.NewProjectID(), Name: binding.Name(), DefaultRoomID: binding.RoomID(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return ProjectView{}, err
	}
	room, err := domain.NewRoom(domain.RoomParams{
		ID: binding.RoomID(), ProjectID: project.ID(), OwnershipKind: domain.RoomOwnershipProject, Name: "General", Description: projectRoomDescription, WorkspaceRoot: binding.LocalLocator(),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return ProjectView{}, err
	}
	if err := tx.InsertProject(ctx, project); err != nil {
		return ProjectView{}, err
	}
	if err := tx.InsertRoom(ctx, room); err != nil {
		return ProjectView{}, err
	}
	contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: s.deps.IDs.ContextEntryID(), RevisionID: s.deps.IDs.ContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief,
		RevisionNumber: 1, Title: "Room Brief", Body: room.Description(), Locator: "room://" + room.ID().String() + "/brief", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return ProjectView{}, err
	}
	initialRevision, err := domain.NewRoomRevision(contextRevision, domain.HumanRoomProvenance(actorID), now)
	if err != nil {
		return ProjectView{}, err
	}
	if err := tx.InsertRevision(ctx, contextRevision); err != nil {
		return ProjectView{}, err
	}
	if err := tx.InsertRoomRevision(ctx, initialRevision); err != nil {
		return ProjectView{}, err
	}
	if err := tx.InsertRepositoryBinding(ctx, binding); err != nil {
		return ProjectView{}, err
	}
	room, err = tx.GetRoom(ctx, room.ID())
	if err != nil {
		return ProjectView{}, err
	}
	return ProjectView{Project: project, Rooms: []domain.Room{room}, Room: room, RepositoryBinding: binding, LastActivityAt: now, TaskCounts: storecontract.RoomTaskCounts{}}, nil
}

func (s *Service) RemoveProject(ctx context.Context, request RemoveProjectRequest) (RemoveProjectResult, error) {
	if err := s.requireProjectStore(); err != nil {
		return RemoveProjectResult{}, err
	}
	if !request.RoomID.Valid() || request.ExpectedVersion == 0 {
		return RemoveProjectResult{}, fmt.Errorf("%w: invalid RemoveProject request", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "remove_project", request.RoomID.String(), request.ExpectedVersion); err != nil {
		return RemoveProjectResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "remove_project", request.RoomID.String(), request.ExpectedVersion, struct{}{}, now)
	if err != nil {
		return RemoveProjectResult{}, err
	}
	var result RemoveProjectResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			var wire removeProjectWire
			if err := decodeResponse(response, &wire); err != nil {
				return err
			}
			room, err := restoreRoomWire(wire.Room)
			if err != nil {
				return err
			}
			result = RemoveProjectResult{Room: room, Replayed: true}
			return nil
		}
		room, err := tx.GetRoom(ctx, request.RoomID)
		if err != nil {
			return err
		}
		if room.OwnershipKind() != domain.RoomOwnershipProject {
			return ErrProjectNotFound
		}
		project, err := tx.GetProject(ctx, room.ProjectID())
		if err != nil {
			return err
		}
		// Legacy writable aliases name only the original default Room.
		if project.DefaultRoomID() != request.RoomID {
			return ErrProjectNotFound
		}
		binding, err := tx.GetRepositoryBinding(ctx, request.RoomID)
		if err != nil {
			return err
		}
		if binding.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		nextBinding, err := binding.Remove(request.ExpectedVersion, now)
		if err != nil {
			return err
		}
		if err := tx.SaveRepositoryBindingCAS(ctx, request.ExpectedVersion, nextBinding); err != nil {
			return err
		}
		nextProject, err := project.Archive(project.Version(), now)
		if err != nil {
			return err
		}
		if err = tx.SaveProjectCAS(ctx, project.Version(), nextProject); err != nil {
			return err
		}
		result = RemoveProjectResult{Room: room}
		response, err = jsonResponse(removeProjectWire{Room: wireRoom(room, "")})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) archiveRoomWithinTx(ctx context.Context, tx storecontract.WriteTx, roomID domain.RoomID, actorID, sessionID string, key storecontract.CommandKey, now time.Time) (domain.Room, error) {
	current, err := tx.GetRoom(ctx, roomID)
	if err != nil {
		return domain.Room{}, err
	}
	next, err := current.Archive(current.Version(), now)
	if err != nil {
		return domain.Room{}, err
	}
	event := storecontract.RoomLifecycleEvent{
		RoomID: next.ID(), FromState: current.State(), ToState: next.State(), Version: next.Version(),
		ActorID: actorID, SessionID: sessionID, IdempotencyKeyHash: key.KeyHash, RequestDigest: key.RequestDigest, OccurredAt: now,
	}
	if err := tx.SaveRoomLifecycleCAS(ctx, current.Version(), next, event); err != nil {
		return domain.Room{}, err
	}
	return next, nil
}

func (s *Service) ListProjects(ctx context.Context, filter string) ([]ProjectView, error) {
	return s.ListProjectsByState(ctx, filter, domain.ProjectStateActive)
}

func (s *Service) ListProjectsByState(ctx context.Context, filter string, state domain.ProjectState) ([]ProjectView, error) {
	if err := s.requireProjectStore(); err != nil {
		return nil, err
	}
	projects, err := s.deps.Store.Reader().ListProjects(ctx, state)
	if err != nil {
		return nil, err
	}
	result := make([]ProjectView, 0, len(projects))
	for _, projectRecord := range projects {
		project, err := s.GetProjectByID(ctx, projectRecord.ID())
		if err != nil {
			return nil, err
		}
		if filter != "" && !strings.Contains(strings.ToLower(project.Project.Name()), strings.ToLower(filter)) {
			continue
		}
		result = append(result, project)
	}
	return result, nil
}

func (s *Service) GetProject(ctx context.Context, roomID domain.RoomID) (ProjectView, error) {
	if err := s.requireProjectStore(); err != nil {
		return ProjectView{}, err
	}
	if !roomID.Valid() {
		return ProjectView{}, ErrProjectNotFound
	}
	room, err := s.deps.Store.Reader().GetRoom(ctx, roomID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return ProjectView{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectView{}, err
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject || !room.ProjectID().Valid() {
		return ProjectView{}, ErrProjectNotFound
	}
	return s.projectForRoom(ctx, room)
}

func (s *Service) GetProjectByID(ctx context.Context, projectID domain.ProjectID) (ProjectView, error) {
	if err := s.requireProjectStore(); err != nil {
		return ProjectView{}, err
	}
	if !projectID.Valid() {
		return ProjectView{}, ErrProjectNotFound
	}
	project, err := s.deps.Store.Reader().GetProject(ctx, projectID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return ProjectView{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectView{}, err
	}
	room, err := s.deps.Store.Reader().GetRoom(ctx, project.DefaultRoomID())
	if err != nil {
		return ProjectView{}, err
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject || room.ProjectID() != project.ID() {
		return ProjectView{}, ErrProjectNotFound
	}
	return s.projectForRoom(ctx, room)
}

func (s *Service) projectForRoom(ctx context.Context, room domain.Room) (ProjectView, error) {
	project, err := s.deps.Store.Reader().GetProject(ctx, room.ProjectID())
	if err != nil {
		return ProjectView{}, err
	}
	binding, err := s.deps.Store.Reader().GetRepositoryBinding(ctx, room.ID())
	if err != nil && !errors.Is(err, storecontract.ErrNotFound) {
		return ProjectView{}, err
	}
	rooms, err := s.deps.Store.Reader().ListProjectRooms(ctx, project.ID())
	if err != nil {
		return ProjectView{}, err
	}
	var action *CurrentAction
	var title string
	var actionAt, lastActivity time.Time
	var counts storecontract.RoomTaskCounts
	for _, ownedRoom := range rooms {
		projection, err := s.deps.Store.Reader().GetRoomWorkspace(ctx, ownedRoom.ID())
		if err != nil {
			return ProjectView{}, err
		}
		if projection.Room.LastActivityAt.After(lastActivity) {
			lastActivity = projection.Room.LastActivityAt
		}
		counts.Total += projection.Room.TaskCounts.Total
		counts.Open += projection.Room.TaskCounts.Open
		counts.Terminal += projection.Room.TaskCounts.Terminal
		effectiveState := projection.Room.Room.State()
		if project.State() == domain.ProjectStateArchived {
			effectiveState = domain.RoomStateArchived
		}
		for _, item := range projection.Tasks {
			if item.Task.Archived() {
				continue
			}
			candidate := deriveCurrentAction(effectiveState, item)
			if candidate.Kind == CurrentActionNone {
				continue
			}
			if action == nil || item.LastActivityAt.After(actionAt) || (item.LastActivityAt.Equal(actionAt) && item.Task.ID().String() < action.Target.TaskID.String()) {
				action, title, actionAt = &candidate, item.Task.Title(), item.LastActivityAt
			}
		}
	}
	return ProjectView{
		Project: project, Rooms: rooms,
		CurrentAction: action, CurrentTaskTitle: title,
		Room:              room,
		RepositoryBinding: binding,
		LastActivityAt:    lastActivity,
		TaskCounts:        counts,
	}, nil
}

func (s *Service) rejectOverlap(ctx context.Context, canonical string) error {
	bindings, err := listAllRepositoryBindings(ctx, s.deps.Store.Reader())
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		other := binding.LocalLocator()
		if pathContains(other, canonical) || pathContains(canonical, other) {
			return ErrProjectOverlap
		}
	}
	return nil
}

type repositoryBindingLister interface {
	ListRepositoryBindings(context.Context, domain.RepositoryBindingState) ([]domain.RepositoryBinding, error)
}

func listAllRepositoryBindings(ctx context.Context, lister repositoryBindingLister) ([]domain.RepositoryBinding, error) {
	active, err := lister.ListRepositoryBindings(ctx, domain.RepositoryBindingStateActive)
	if err != nil {
		return nil, err
	}
	removed, err := lister.ListRepositoryBindings(ctx, domain.RepositoryBindingStateRemoved)
	if err != nil {
		return nil, err
	}
	return append(active, removed...), nil
}

func pathContains(root, child string) bool {
	if root == child {
		return true
	}
	relative, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s *Service) requireProjectStore() error {
	if s == nil || s.deps.Store == nil {
		return fmt.Errorf("%w: missing store", ErrInvalidCommand)
	}
	return nil
}

func mapInspectError(err error) error {
	var pathErr *gitsource.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%w: %s", ErrRepositoryNotGit, pathErr.Error())
	}
	switch {
	case errors.Is(err, gitsource.ErrBare):
		return ErrRepositoryBare
	case errors.Is(err, gitsource.ErrLinkedWorktree):
		return ErrRepositoryLinkedWorktree
	case errors.Is(err, gitsource.ErrNotGit):
		return ErrRepositoryNotGit
	default:
		return err
	}
}

func baseIdentityFor(commit, tree string) string {
	return "sha:" + commit + ":tree:" + tree
}

func sanitizedRepoBaseName(path string) string {
	return sanitizeProjectName(filepath.Base(filepath.Clean(path)))
}

func sanitizeURLBasename(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	base := value
	if index := strings.LastIndex(base, "/"); index >= 0 {
		base = base[index+1:]
	}
	base = strings.TrimSuffix(base, ".git")
	return sanitizeProjectName(base)
}

func sanitizeProjectName(name string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', 0:
		default:
			builder.WriteRune(r)
		}
	}
	cleaned := strings.TrimLeft(strings.TrimSpace(builder.String()), ".")
	if cleaned == "" {
		return "repository"
	}
	return cleaned
}

type repositoryBindingWire struct {
	RoomID         string                        `json:"roomId"`
	Name           string                        `json:"name"`
	LocalLocator   string                        `json:"localLocator"`
	SourceKind     domain.RepositorySourceKind   `json:"sourceKind"`
	CloneURL       string                        `json:"cloneUrl"`
	AdmittedBase   string                        `json:"admittedBase"`
	BaseIdentity   string                        `json:"baseIdentity"`
	TargetWorktree string                        `json:"targetWorktree"`
	DirtyAdmitted  bool                          `json:"dirtyAdmitted"`
	State          domain.RepositoryBindingState `json:"state"`
	Version        uint64                        `json:"version"`
	CreatedAt      time.Time                     `json:"createdAt"`
	UpdatedAt      time.Time                     `json:"updatedAt"`
}

func wireRepositoryBinding(binding domain.RepositoryBinding) repositoryBindingWire {
	return repositoryBindingWire{
		RoomID: binding.RoomID().String(), Name: binding.Name(), LocalLocator: binding.LocalLocator(),
		SourceKind: binding.SourceKind(), CloneURL: binding.CloneURL(), AdmittedBase: binding.AdmittedBase(),
		BaseIdentity: binding.BaseIdentity(), TargetWorktree: binding.TargetWorktree(), DirtyAdmitted: binding.DirtyAdmitted(),
		State: binding.State(), Version: binding.Version(), CreatedAt: binding.CreatedAt(), UpdatedAt: binding.UpdatedAt(),
	}
}

func restoreRepositoryBindingWire(wire repositoryBindingWire) (domain.RepositoryBinding, error) {
	roomID, err := domain.ParseRoomID(wire.RoomID)
	if err != nil {
		return domain.RepositoryBinding{}, err
	}
	return domain.RestoreRepositoryBinding(domain.RepositoryBindingRecord{
		RoomID: roomID, Name: wire.Name, LocalLocator: wire.LocalLocator, SourceKind: wire.SourceKind,
		CloneURL: wire.CloneURL, AdmittedBase: wire.AdmittedBase, BaseIdentity: wire.BaseIdentity,
		TargetWorktree: wire.TargetWorktree, DirtyAdmitted: wire.DirtyAdmitted, State: wire.State,
		Version: wire.Version, CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt,
	})
}

type projectWire struct {
	Project           projectRecordWire            `json:"project"`
	Rooms             []roomWire                   `json:"rooms"`
	Room              roomWire                     `json:"room"`
	RepositoryBinding repositoryBindingWire        `json:"repositoryBinding"`
	LastActivityAt    time.Time                    `json:"lastActivityAt"`
	TaskCounts        storecontract.RoomTaskCounts `json:"taskCounts"`
}

type projectRecordWire struct {
	ID, Name, DefaultRoomID          string
	State                            domain.ProjectState
	Version                          uint64
	CreatedAt, UpdatedAt, ArchivedAt time.Time
}

func wireProjectRecord(project domain.Project) projectRecordWire {
	return projectRecordWire{ID: project.ID().String(), Name: project.Name(), DefaultRoomID: project.DefaultRoomID().String(), State: project.State(), Version: project.Version(), CreatedAt: project.CreatedAt(), UpdatedAt: project.UpdatedAt(), ArchivedAt: project.ArchivedAt()}
}
func restoreProjectRecordWire(w projectRecordWire) (domain.Project, error) {
	id, err := domain.ParseProjectID(w.ID)
	if err != nil {
		return domain.Project{}, err
	}
	roomID, err := domain.ParseRoomID(w.DefaultRoomID)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.RestoreProject(domain.ProjectRecord{ID: id, Name: w.Name, DefaultRoomID: roomID, State: w.State, Version: w.Version, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, ArchivedAt: w.ArchivedAt})
}

func wireProject(project ProjectView) projectWire {
	rooms := make([]roomWire, 0, len(project.Rooms))
	for _, room := range project.Rooms {
		rooms = append(rooms, wireRoom(room, ""))
	}
	return projectWire{
		Project: wireProjectRecord(project.Project), Rooms: rooms,
		Room: wireRoom(project.Room, ""), RepositoryBinding: wireRepositoryBinding(project.RepositoryBinding),
		LastActivityAt: project.LastActivityAt, TaskCounts: project.TaskCounts,
	}
}

func restoreProjectWire(wire projectWire) (ProjectView, error) {
	room, err := restoreRoomWire(wire.Room)
	if err != nil {
		return ProjectView{}, err
	}
	binding, err := restoreRepositoryBindingWire(wire.RepositoryBinding)
	if err != nil {
		return ProjectView{}, err
	}
	project, err := restoreProjectRecordWire(wire.Project)
	if err != nil {
		return ProjectView{}, err
	}
	rooms := make([]domain.Room, 0, len(wire.Rooms))
	for _, roomWire := range wire.Rooms {
		restored, err := restoreRoomWire(roomWire)
		if err != nil {
			return ProjectView{}, err
		}
		rooms = append(rooms, restored)
	}
	if len(rooms) == 0 {
		rooms = []domain.Room{room}
	}
	return ProjectView{Project: project, Rooms: rooms, Room: room, RepositoryBinding: binding, LastActivityAt: wire.LastActivityAt, TaskCounts: wire.TaskCounts}, nil
}

type removeProjectWire struct {
	Room roomWire `json:"room"`
}
