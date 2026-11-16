package store

import (
	"context"
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
)

type WorkflowReader interface {
	GetWorkflow(context.Context, string) (domain.WorkflowExecution, error)
	GetTaskWorkflow(context.Context, domain.TaskID) (domain.WorkflowExecution, error)
	ListActiveWorkflows(context.Context) ([]domain.WorkflowExecution, error)
	ListWorkflowCommands(context.Context, string) ([]domain.WorkflowCommand, error)
	ListWorkflowEvents(context.Context, string, uint64) ([]domain.WorkflowEvent, error)
}
type WorkflowWriter interface {
	InsertWorkflow(context.Context, domain.WorkflowExecution) error
	SaveWorkflowCAS(context.Context, uint64, domain.WorkflowExecution) error
	PutWorkflowCommand(context.Context, domain.WorkflowCommand) error
	RecordWorkflowReceipt(context.Context, domain.WorkflowCommand, json.RawMessage) error
	AppendWorkflowEvent(context.Context, string, string, json.RawMessage) error
}
