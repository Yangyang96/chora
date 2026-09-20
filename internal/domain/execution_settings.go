package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

const maxNativeCapabilitiesJSONBytes = 64 * 1024

const (
	ExecutionSettingsSourceProject = "project"
	ExecutionSettingsSourceDefault = "default"
	ExecutionSettingsSourceTask    = "task"
)

// ProjectExecutionSettings stores the reusable execution defaults for a Project.
// A nil Model delegates model selection to the selected Runtime.
type ProjectExecutionSettings struct {
	ProjectID             ProjectID             `json:"projectId"`
	Version               uint64                `json:"version"`
	AgentExecutionProfile AgentExecutionProfile `json:"agentExecutionProfile"`
	Model                 *ModelIdentity        `json:"model"`
	UpdatedAt             time.Time             `json:"updatedAt"`
}

func (settings ProjectExecutionSettings) Validate() error {
	if !settings.ProjectID.Valid() || settings.Version == 0 || settings.UpdatedAt.IsZero() ||
		!currentExecutionSettingsProfile(settings.AgentExecutionProfile) {
		return fmt.Errorf("%w: invalid Project execution settings", ErrInvalidArgument)
	}
	if settings.Model != nil && (!modelIdentifier(settings.Model.Provider) || !modelIdentifier(settings.Model.ModelID)) {
		return fmt.Errorf("%w: invalid Project execution model", ErrInvalidArgument)
	}
	return nil
}

// TaskExecutionSettings is the immutable effective execution configuration
// resolved when a Task is created.
type TaskExecutionSettings struct {
	TaskID                 TaskID                `json:"taskId"`
	ProjectID              ProjectID             `json:"projectId"`
	ProjectVersion         uint64                `json:"projectVersion"`
	AgentExecutionProfile  AgentExecutionProfile `json:"agentExecutionProfile"`
	EnvironmentSource      string                `json:"environmentSource"`
	ModelSource            string                `json:"modelSource"`
	ModelBinding           ModelBinding          `json:"modelBinding"`
	NativeCapabilitiesJSON string                `json:"nativeCapabilitiesJson"`
	CreatedAt              time.Time             `json:"createdAt"`
}

func (settings TaskExecutionSettings) Validate() error {
	if !settings.TaskID.Valid() || !settings.ProjectID.Valid() || settings.CreatedAt.IsZero() ||
		!currentExecutionSettingsProfile(settings.AgentExecutionProfile) ||
		!validExecutionSettingsSource(settings.EnvironmentSource) || !validExecutionSettingsSource(settings.ModelSource) {
		return fmt.Errorf("%w: invalid Task execution settings", ErrInvalidArgument)
	}
	if settings.ModelBinding.Configured() {
		parsed, err := ParseModelBinding(settings.ModelBinding.JSON())
		if err != nil || parsed.JSON() != settings.ModelBinding.JSON() {
			return fmt.Errorf("%w: invalid Task execution model binding", ErrInvalidArgument)
		}
	}
	if len(settings.NativeCapabilitiesJSON) > maxNativeCapabilitiesJSONBytes || !validJSONObject(settings.NativeCapabilitiesJSON) {
		return fmt.Errorf("%w: invalid native capability snapshot", ErrInvalidArgument)
	}
	return nil
}

func currentExecutionSettingsProfile(profile AgentExecutionProfile) bool {
	return profile == AgentExecutionProfileTrustedLocal || profile == AgentExecutionProfileIsolatedLocal
}

func validExecutionSettingsSource(source string) bool {
	return source == ExecutionSettingsSourceProject || source == ExecutionSettingsSourceDefault || source == ExecutionSettingsSourceTask
}

func validJSONObject(raw string) bool {
	if raw == "" || !json.Valid([]byte(raw)) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(raw), &object) == nil && object != nil
}
