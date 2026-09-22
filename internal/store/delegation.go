package store

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
)

type DelegationReader interface {
	GetDelegationSynthesis(context.Context, domain.TaskID) (domain.DelegationSynthesis, error)
	GetSynthesisForTask(context.Context, domain.TaskID) (domain.DelegationSynthesis, error)
	GetDelegationPlanning(context.Context, domain.TaskID) (domain.DelegationPlanningIntent, error)
	ListActiveDelegationPlanning(context.Context) ([]domain.DelegationPlanningIntent, error)
	GetDelegationProposalSource(context.Context, domain.TaskID) (domain.DelegationProposalSource, error)
	GetTaskDelegation(context.Context, domain.TaskID) (domain.TaskDelegation, error)
	ListActiveTaskDelegations(context.Context) ([]domain.TaskDelegation, error)
	ListDelegationChildren(context.Context, domain.TaskID) ([]domain.DelegationChild, error)
	GetDelegationChild(context.Context, domain.TaskID) (domain.DelegationChild, error)
}
type DelegationWriter interface {
	InsertDelegationSynthesis(context.Context, domain.DelegationSynthesis) error
	BindSynthesisTask(context.Context, domain.TaskID, domain.TaskID, []domain.DelegationSynthesisInput) error
	BindSynthesisRun(context.Context, domain.TaskID, domain.RunID) error
	BindSynthesisAttempt(context.Context, domain.TaskID, domain.AttemptID) error
	InsertDelegationPlanning(context.Context, domain.DelegationPlanningIntent) error
	SaveDelegationPlanningCAS(context.Context, uint64, domain.DelegationPlanningIntent) error
	InsertDelegationProposalSource(context.Context, domain.DelegationProposalSource) error
	InsertTaskDelegation(context.Context, domain.TaskDelegation) error
	SaveTaskDelegationCAS(context.Context, uint64, domain.TaskDelegation) error
	InsertDelegationChild(context.Context, domain.DelegationChild) error
	BindDelegationChildRun(context.Context, domain.TaskID, domain.RunID) error
}
