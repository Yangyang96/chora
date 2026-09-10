package store

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
	"time"
)

type TaskResourceRecord struct {
	TaskID        domain.TaskID
	CanonicalJSON []byte
	Digest        [32]byte
	CreatedAt     time.Time
}
type ResourceReader interface {
	GetResourceApplyOperation(context.Context, domain.ResultID) (ResourceApplyOperation, error)
	ListResourceApplySteps(context.Context, string) ([]ResourceApplyStep, error)
	GetResourceResultReview(context.Context, domain.ResultID) (domain.ReviewDecision, [32]byte, error)
	GetResourceResultGroup(context.Context, domain.AttemptID) (domain.ResourceResultGroup, error)
	ListRepositoryChecks(context.Context, domain.RepositoryID) ([]domain.RepositoryCheckDefinition, error)
	ListTaskRepositoryWorktrees(context.Context, domain.TaskID) ([]domain.TaskRepositoryWorktree, error)
	ListResourceProjects(context.Context, domain.ProjectState, string, int) ([]domain.Project, error)
	GetRepository(context.Context, domain.RepositoryID) (domain.RepositoryRecord, error)
	GetRepositoryByCheckout(context.Context, string) (domain.RepositoryRecord, error)
	ListProjectRepositories(context.Context, domain.ProjectID, string, int) ([]domain.ProjectRepository, error)
	GetProjectRepository(context.Context, domain.ProjectID, domain.RepositoryID) (domain.ProjectRepository, error)
	GetRoomRepositoryReferences(context.Context, domain.RoomID) (domain.RoomRepositoryReferences, error)
	GetTaskResourceSnapshot(context.Context, domain.TaskID) (TaskResourceRecord, error)
	RepositoryHasPendingWork(context.Context, domain.RepositoryID) (bool, error)
}
type ResourceWriter interface {
	InsertResourceApplyOperation(context.Context, ResourceApplyOperation) error
	AppendResourceApplyStep(context.Context, uint64, ResourceApplyStep) error
	InsertResourceResultReview(context.Context, domain.ResultID, [32]byte, domain.ReviewDecisionID) error
	InsertResourceResultGroup(context.Context, domain.ResourceResultGroup) error
	InsertRepositoryCheck(context.Context, uint64, domain.RepositoryCheckDefinition) error
	SaveTaskRepositoryWorktree(context.Context, uint64, domain.TaskRepositoryWorktree) error
	InsertRepository(context.Context, domain.RepositoryRecord) error
	VerifyLegacyRepository(context.Context, domain.RepositoryRecord) error
	SaveProjectRepository(context.Context, uint64, domain.ProjectRepository) error
	SaveRoomRepositoryReferences(context.Context, uint64, domain.RoomRepositoryReferences) error
	InsertTaskResourceSnapshot(context.Context, TaskResourceRecord) error
}

type ResourceApplyOperation struct {
	ID            string
	ResultID      domain.ResultID
	CanonicalJSON []byte
	CreatedAt     time.Time
}
type ResourceApplyStep struct {
	OperationID   string
	RepositoryID  domain.RepositoryID
	Sequence      uint64
	Status        string
	CanonicalJSON []byte
	CreatedAt     time.Time
}
