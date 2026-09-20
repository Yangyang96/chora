package store

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
)

type ExecutionSettingsReader interface {
	GetProjectExecutionSettings(context.Context, domain.ProjectID) (domain.ProjectExecutionSettings, error)
	GetTaskExecutionSettings(context.Context, domain.TaskID) (domain.TaskExecutionSettings, error)
}

type ExecutionSettingsWriter interface {
	SaveProjectExecutionSettings(context.Context, uint64, domain.ProjectExecutionSettings) error
	InsertTaskExecutionSettings(context.Context, domain.TaskExecutionSettings) error
}
