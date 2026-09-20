package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (reader *reader) GetProjectExecutionSettings(ctx context.Context, projectID domain.ProjectID) (domain.ProjectExecutionSettings, error) {
	var projectIDText, profile, updatedAtText string
	var version uint64
	var provider, modelID sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT project_id,version,agent_execution_profile,model_provider,model_id,updated_at
		FROM project_execution_settings WHERE project_id=?`, projectID.String()).
		Scan(&projectIDText, &version, &profile, &provider, &modelID, &updatedAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectExecutionSettings{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	parsedProjectID, err := domain.ParseProjectID(projectIDText)
	if err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	updatedAt, err := parseTime(updatedAtText)
	if err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	var model *domain.ModelIdentity
	if provider.Valid || modelID.Valid {
		if !provider.Valid || !modelID.Valid {
			return domain.ProjectExecutionSettings{}, fmt.Errorf("decode Project execution settings: partial model identity")
		}
		model = &domain.ModelIdentity{Provider: provider.String, ModelID: modelID.String}
	}
	settings := domain.ProjectExecutionSettings{
		ProjectID: parsedProjectID, Version: version, AgentExecutionProfile: domain.AgentExecutionProfile(profile),
		Model: model, UpdatedAt: updatedAt,
	}
	if err := settings.Validate(); err != nil {
		return domain.ProjectExecutionSettings{}, fmt.Errorf("decode Project execution settings: %w", err)
	}
	return settings, nil
}

func (tx *writeTx) SaveProjectExecutionSettings(ctx context.Context, expected uint64, settings domain.ProjectExecutionSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	if settings.Version != expected+1 {
		return fmt.Errorf("%w: invalid Project execution settings successor", domain.ErrInvalidArgument)
	}
	var provider, modelID any
	if settings.Model != nil {
		provider, modelID = settings.Model.Provider, settings.Model.ModelID
	}
	if expected == 0 {
		if _, err := tx.GetProject(ctx, settings.ProjectID); err != nil {
			return err
		}
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO project_execution_settings(project_id,version,agent_execution_profile,model_provider,model_id,updated_at)
			VALUES(?,?,?,?,?,?) ON CONFLICT(project_id) DO NOTHING`, settings.ProjectID.String(), settings.Version,
			string(settings.AgentExecutionProfile), provider, modelID, timeText(settings.UpdatedAt))
		if err != nil {
			return mapWriteError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return storecontract.ErrVersionConflict
		}
		return nil
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE project_execution_settings
		SET version=?,agent_execution_profile=?,model_provider=?,model_id=?,updated_at=?
		WHERE project_id=? AND version=?`, settings.Version, string(settings.AgentExecutionProfile), provider, modelID,
		timeText(settings.UpdatedAt), settings.ProjectID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func (reader *reader) GetTaskExecutionSettings(ctx context.Context, taskID domain.TaskID) (domain.TaskExecutionSettings, error) {
	var taskIDText, projectIDText, profile, environmentSource, modelSource, nativeCapabilitiesJSON, createdAtText string
	var projectVersion uint64
	var modelBinding sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT task_id,project_id,project_version,agent_execution_profile,environment_source,
		model_source,model_binding,native_capabilities_json,created_at FROM task_execution_settings WHERE task_id=?`, taskID.String()).
		Scan(&taskIDText, &projectIDText, &projectVersion, &profile, &environmentSource, &modelSource, &modelBinding,
			&nativeCapabilitiesJSON, &createdAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskExecutionSettings{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.TaskExecutionSettings{}, err
	}
	parsedTaskID, err := domain.ParseTaskID(taskIDText)
	if err != nil {
		return domain.TaskExecutionSettings{}, err
	}
	projectID, err := domain.ParseProjectID(projectIDText)
	if err != nil {
		return domain.TaskExecutionSettings{}, err
	}
	binding, err := domain.ParseModelBinding(modelBinding.String)
	if err != nil {
		return domain.TaskExecutionSettings{}, err
	}
	createdAt, err := parseTime(createdAtText)
	if err != nil {
		return domain.TaskExecutionSettings{}, err
	}
	settings := domain.TaskExecutionSettings{
		TaskID: parsedTaskID, ProjectID: projectID, ProjectVersion: projectVersion,
		AgentExecutionProfile: domain.AgentExecutionProfile(profile), EnvironmentSource: environmentSource,
		ModelSource: modelSource, ModelBinding: binding, NativeCapabilitiesJSON: nativeCapabilitiesJSON, CreatedAt: createdAt,
	}
	if err := settings.Validate(); err != nil {
		return domain.TaskExecutionSettings{}, fmt.Errorf("decode Task execution settings: %w", err)
	}
	return settings, nil
}

func (tx *writeTx) InsertTaskExecutionSettings(ctx context.Context, settings domain.TaskExecutionSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	var binding any
	if settings.ModelBinding.Configured() {
		binding = settings.ModelBinding.JSON()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO task_execution_settings(task_id,project_id,project_version,
		agent_execution_profile,environment_source,model_source,model_binding,native_capabilities_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, settings.TaskID.String(), settings.ProjectID.String(), settings.ProjectVersion,
		string(settings.AgentExecutionProfile), settings.EnvironmentSource, settings.ModelSource, binding,
		settings.NativeCapabilitiesJSON, timeText(settings.CreatedAt))
	return mapWriteError(err)
}
