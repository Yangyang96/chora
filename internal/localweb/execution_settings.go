package localweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type projectExecutionSettingsView struct {
	ProjectID             string                       `json:"projectId"`
	Version               uint64                       `json:"version"`
	AgentExecutionProfile domain.AgentExecutionProfile `json:"agentExecutionProfile"`
	Model                 *domain.ModelIdentity        `json:"model"`
	UpdatedAt             time.Time                    `json:"updatedAt"`
}

func projectExecutionSettingsViewOf(value domain.ProjectExecutionSettings) projectExecutionSettingsView {
	return projectExecutionSettingsView{value.ProjectID.String(), value.Version, value.AgentExecutionProfile, value.Model, value.UpdatedAt}
}

func (server *Server) getProjectExecutionSettings(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	value, err := server.service.GetProjectExecutionSettings(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectExecutionSettingsViewOf(value))
}

func (server *Server) putProjectExecutionSettings(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		Version               uint64                       `json:"version"`
		AgentExecutionProfile domain.AgentExecutionProfile `json:"agentExecutionProfile"`
		Model                 *domain.ModelIdentity        `json:"model"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !currentExecutionEnvironment(input.AgentExecutionProfile) {
		writeError(w, http.StatusBadRequest, errors.New("select local execution or isolated execution"))
		return
	}
	value, err := server.service.UpdateProjectExecutionSettings(r.Context(), app.UpdateProjectExecutionSettingsRequest{
		CommandMeta: requestCommandMeta(r, "update-project-execution-settings"),
		ProjectID:   id, ExpectedVersion: input.Version, AgentExecutionProfile: input.AgentExecutionProfile, Model: input.Model, ResolveExecutionModel: server.resolveExecutionModel,
	})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectExecutionSettingsViewOf(value))
}

func (server *Server) resolveExecutionModel(ctx context.Context, profile domain.AgentExecutionProfile, model *domain.ModelIdentity) (domain.ModelBinding, error) {
	if !currentExecutionEnvironment(profile) {
		return domain.ModelBinding{}, fmt.Errorf("%w: unsupported execution environment", app.ErrInvalidCommand)
	}
	if profile == domain.AgentExecutionProfileIsolatedLocal {
		if err := server.isolatedLocal.available(ctx); err != nil {
			return domain.ModelBinding{}, fmt.Errorf("%w: isolated execution is unavailable; prepare it before starting", app.ErrInvalidCommand)
		}
	}
	if model == nil {
		return domain.ModelBinding{}, nil
	}
	var catalog domain.ModelCatalog
	var err error
	if profile == domain.AgentExecutionProfileIsolatedLocal {
		catalog, err = server.isolatedLocal.modelCatalog(ctx)
	} else {
		catalog, err = pathModelCatalog(ctx, server.piDiscoveryOptions)
	}
	if err == nil {
		err = agentpi.ValidateExactModelArguments(catalog, *model)
	}
	if err != nil {
		return domain.ModelBinding{}, fmt.Errorf("%w: selected model is unavailable in this execution environment; choose an available model or explicitly use the Runtime default", app.ErrInvalidCommand)
	}
	return domain.NewModelBinding(catalog, *model, time.Now().UTC())
}

// Only selection provenance is exposed; native configuration references stay
// in the private task snapshot and are consumed by the runtime integration.
type taskExecutionSettingsView struct {
	ProjectID             string                       `json:"projectId"`
	ProjectVersion        uint64                       `json:"projectVersion"`
	AgentExecutionProfile domain.AgentExecutionProfile `json:"agentExecutionProfile"`
	EnvironmentSource     string                       `json:"environmentSource"`
	ModelSource           string                       `json:"modelSource"`
	ModelBinding          domain.ModelBinding          `json:"modelBinding"`
}

func (server *Server) taskExecutionSettingsView(ctx context.Context, taskID domain.TaskID) (*taskExecutionSettingsView, error) {
	value, err := server.store.Reader().GetTaskExecutionSettings(ctx, taskID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &taskExecutionSettingsView{ProjectID: value.ProjectID.String(), ProjectVersion: value.ProjectVersion,
		AgentExecutionProfile: value.AgentExecutionProfile, EnvironmentSource: value.EnvironmentSource,
		ModelSource: value.ModelSource, ModelBinding: value.ModelBinding}, nil
}
