package localweb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// taskWorktreeResolver resolves a Task's durable worktree against the Task's
// Room-bound repository, lazily building and caching one taskWorktreeManager
// per repository anchor and immutable base. Only positively classified legacy
// M1/Fake Rooms retain the process-global manager; missing ownership fails closed.
type taskWorktreeResolver struct {
	reader   storecontract.Reader
	fallback *taskWorktreeManager

	mu       sync.Mutex
	byAnchor map[string]*taskWorktreeManager
}

func newTaskWorktreeResolver(reader storecontract.Reader, fallback *taskWorktreeManager) *taskWorktreeResolver {
	return &taskWorktreeResolver{reader: reader, fallback: fallback, byAnchor: make(map[string]*taskWorktreeManager)}
}

// Plan selects the current committed base for a new Task. Inside a write
// transaction it reads only the committed Project and Git; not the new Task.
func (resolver *taskWorktreeResolver) Plan(ctx context.Context, task domain.Task, createdAt time.Time) (domain.TaskWorktreeBinding, error) {
	if resolver == nil {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: resolver is not configured", app.ErrTaskWorktreeUnavailable)
	}
	return resolver.PlanForRevision(ctx, task, "", createdAt)
}

// Ensure resolves the Project identity and the supplied immutable Task base.
// It never substitutes the current checkout HEAD or refreshes a historical base.
func (resolver *taskWorktreeResolver) Ensure(ctx context.Context, binding domain.TaskWorktreeBinding) error {
	if resolver == nil {
		return fmt.Errorf("%w: resolver is not configured", app.ErrTaskWorktreeUnavailable)
	}
	task, err := resolver.reader.GetTask(ctx, binding.TaskID())
	if err != nil {
		return err
	}
	manager, err := resolver.managerForTaskBase(ctx, task.RoomID(), binding)
	if err != nil {
		return err
	}
	if manager == nil {
		return fmt.Errorf("%w: manager is not configured", app.ErrTaskWorktreeUnavailable)
	}
	return manager.Ensure(ctx, binding)
}

// ResolveExecutionRoot returns the proven Task worktree host path for a
// Room-bound repository. A Room without a RepositoryBinding returns "" so the
// caller keeps the charter's logical WorkspaceRoot (the M1 installed path).
func (resolver *taskWorktreeResolver) ResolveExecutionRoot(ctx context.Context, task domain.Task) (string, error) {
	if resolver == nil || resolver.reader == nil {
		return "", fmt.Errorf("%w: resolver reader is unavailable", app.ErrTaskWorktreeUnavailable)
	}
	_, err := resolver.reader.GetRepositoryBinding(ctx, task.RoomID())
	if errors.Is(err, storecontract.ErrNotFound) {
		if err := requireQualifiedLegacyRoom(ctx, resolver.reader, task.RoomID()); err != nil {
			return "", err
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	worktree, err := resolver.reader.GetTaskWorktreeBinding(ctx, task.ID())
	if err != nil {
		return "", err
	}
	manager, err := resolver.managerForTaskBase(ctx, task.RoomID(), worktree)
	if err != nil {
		return "", err
	}
	root, err := manager.ResolveReady(ctx, worktree)
	if err != nil {
		return "", err
	}
	contractBinding, err := resolver.reader.GetSpecCodingBinding(ctx, task.ID())
	// Fake/legacy Tasks have no Spec Coding binding; real admission requires one.
	if errors.Is(err, storecontract.ErrNotFound) {
		return root, nil
	}
	if err != nil {
		return "", err
	}
	if contractBinding.Status == storecontract.SpecCodingRegistered {
		contract, err := speccoding.DecodeCoreContract(contractBinding.ActiveContractJSON)
		if err != nil {
			return "", err
		}
		document := contract.Document()
		for _, path := range document.Execution.Boundary.WritableFiles {
			if err := validateRepositoryScopePath(root, path, false, false); err != nil {
				return "", fmt.Errorf("%w: unsafe execution scope: %v", app.ErrTaskWorktreeUnavailable, err)
			}
		}
		for _, path := range document.Execution.Boundary.WritableDirectories {
			if err := validateRepositoryScopePath(root, path, true, false); err != nil {
				return "", fmt.Errorf("%w: unsafe execution scope: %v", app.ErrTaskWorktreeUnavailable, err)
			}
		}
		for _, command := range document.Execution.Boundary.Commands {
			if command.WorkingDirectory == "" || command.WorkingDirectory == "." {
				continue
			}
			if err := validateRepositoryScopePath(root, command.WorkingDirectory, true, true); err != nil {
				return "", fmt.Errorf("%w: unsafe execution check directory: %v", app.ErrTaskWorktreeUnavailable, err)
			}
		}
	}
	return root, nil
}

func (resolver *taskWorktreeResolver) managerForBinding(binding domain.RepositoryBinding) (*taskWorktreeManager, error) {
	anchor := binding.LocalLocator()
	cacheKey := anchor + "\x00" + binding.Name() + "\x00" + binding.BaseIdentity()
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if manager := resolver.byAnchor[cacheKey]; manager != nil {
		return manager, nil
	}
	manager, err := newTaskWorktreeManager(anchor, speccoding.RepositoryIdentity{
		Name:           taskWorktreeRepositoryName(binding.Name()),
		SourceRevision: binding.AdmittedBase(),
	})
	if err != nil {
		return nil, fmt.Errorf("build Task worktree manager for room %s: %w", binding.RoomID(), err)
	}
	resolver.byAnchor[cacheKey] = manager
	return manager, nil
}

// taskWorktreeRepositoryName derives a safe worktree repository identity from a
// project's admitted repository name. The result satisfies the domain's
// repository-identity and locator constraints (lowercase alphanumeric with
// single-hyphen separators).
func taskWorktreeRepositoryName(name string) string {
	var builder strings.Builder
	separator := false
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if char <= unicode.MaxASCII && ((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
			if separator && builder.Len() > 0 && builder.Len() < taskWorktreeTopicSlugLimit {
				builder.WriteByte('-')
			}
			separator = false
			if builder.Len() < taskWorktreeTopicSlugLimit {
				builder.WriteRune(char)
			}
			continue
		}
		separator = builder.Len() > 0
	}
	value := strings.Trim(builder.String(), "-")
	if value == "" {
		return "repository"
	}
	return value
}

var _ interface {
	Plan(context.Context, domain.Task, time.Time) (domain.TaskWorktreeBinding, error)
	Ensure(context.Context, domain.TaskWorktreeBinding) error
} = (*taskWorktreeResolver)(nil)

func (resolver *taskWorktreeResolver) PlanForRevision(ctx context.Context, task domain.Task, expected string, createdAt time.Time) (domain.TaskWorktreeBinding, error) {
	project, err := resolver.reader.GetRepositoryBinding(ctx, task.RoomID())
	if errors.Is(err, storecontract.ErrNotFound) {
		if err := requireQualifiedLegacyRoom(ctx, resolver.reader, task.RoomID()); err != nil {
			return domain.TaskWorktreeBinding{}, err
		}
		if resolver.fallback == nil {
			return domain.TaskWorktreeBinding{}, app.ErrTaskWorktreeUnavailable
		}
		return resolver.fallback.Plan(task.ID(), task.Title(), createdAt)
	}
	if err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	if project.State() != domain.RepositoryBindingStateActive {
		return domain.TaskWorktreeBinding{}, errors.New("Project is removed")
	}
	revision, tree, ref, err := currentTaskBase(ctx, project.LocalLocator())
	if err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	if expected != "" && revision != expected {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: Project HEAD changed during Task declaration; retry from the selected branch", app.ErrInvalidCommand)
	}
	// A new manager proves root and commit before the binding is persisted.
	manager, err := newTaskWorktreeManager(project.LocalLocator(), speccoding.RepositoryIdentity{Name: taskWorktreeRepositoryName(project.Name()), SourceRevision: revision})
	if err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	return domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: task.ID().String(), RepositoryIdentity: manager.repositoryIdentity, PinnedBaseRevision: revision,
		PinnedBaseTree: tree, BaseRef: ref, StartPolicy: domain.TaskStartPolicyCurrentHEAD,
		RelativeLocator: manager.repositoryIdentity + "-" + task.ID().String() + "-" + taskWorktreeTopicSlug(task.Title()), ConfiguredRootFingerprint: manager.rootFingerprint, CreatedAt: createdAt,
	})
}

func (resolver *taskWorktreeResolver) managerForTaskBase(ctx context.Context, roomID domain.RoomID, base domain.TaskWorktreeBinding) (*taskWorktreeManager, error) {
	project, err := resolver.reader.GetRepositoryBinding(ctx, roomID)
	if errors.Is(err, storecontract.ErrNotFound) {
		if err := requireQualifiedLegacyRoom(ctx, resolver.reader, roomID); err != nil {
			return nil, err
		}
		return resolver.fallback, nil
	}
	if err != nil {
		return nil, err
	}
	projected, err := repositoryForTaskBase(ctx, project, base)
	if err != nil {
		return nil, err
	}
	return resolver.managerForBinding(projected)
}

// Only positively classified M1/Fake Rooms retain the installed execution path.
// Missing Project/resource records are never evidence for this compatibility.
func requireQualifiedLegacyRoom(ctx context.Context, reader storecontract.Reader, roomID domain.RoomID) error {
	room, err := reader.GetRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if string(room.OwnershipKind()) != "legacy_standalone" || room.ProjectID().Valid() {
		return fmt.Errorf("%w: Room has no qualified execution ownership", app.ErrTaskWorktreeUnavailable)
	}
	return nil
}
