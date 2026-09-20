package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type UpdateProjectExecutionSettingsRequest struct {
	CommandMeta
	ProjectID             domain.ProjectID
	ExpectedVersion       uint64
	AgentExecutionProfile domain.AgentExecutionProfile
	Model                 *domain.ModelIdentity
	ResolveExecutionModel func(context.Context, domain.AgentExecutionProfile, *domain.ModelIdentity) (domain.ModelBinding, error)
}

type projectExecutionSettingsWire struct {
	ProjectID             string
	Version               uint64
	AgentExecutionProfile domain.AgentExecutionProfile
	Model                 *domain.ModelIdentity
	UpdatedAt             time.Time
}

func wireProjectExecutionSettings(settings domain.ProjectExecutionSettings) projectExecutionSettingsWire {
	return projectExecutionSettingsWire{ProjectID: settings.ProjectID.String(), Version: settings.Version, AgentExecutionProfile: settings.AgentExecutionProfile, Model: settings.Model, UpdatedAt: settings.UpdatedAt}
}

func restoreProjectExecutionSettingsWire(wire projectExecutionSettingsWire) (domain.ProjectExecutionSettings, error) {
	projectID, err := domain.ParseProjectID(wire.ProjectID)
	if err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	settings := domain.ProjectExecutionSettings{ProjectID: projectID, Version: wire.Version, AgentExecutionProfile: wire.AgentExecutionProfile, Model: wire.Model, UpdatedAt: wire.UpdatedAt}
	return settings, settings.Validate()
}

func defaultProjectExecutionSettings(projectID domain.ProjectID) domain.ProjectExecutionSettings {
	return domain.ProjectExecutionSettings{ProjectID: projectID, AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal}
}

func (s *Service) GetProjectExecutionSettings(ctx context.Context, projectID domain.ProjectID) (domain.ProjectExecutionSettings, error) {
	if s == nil || s.deps.Store == nil || !projectID.Valid() {
		return domain.ProjectExecutionSettings{}, fmt.Errorf("%w: invalid Project execution settings request", ErrInvalidCommand)
	}
	if _, err := s.deps.Store.Reader().GetProject(ctx, projectID); err != nil {
		if errors.Is(err, storecontract.ErrNotFound) {
			return domain.ProjectExecutionSettings{}, ErrProjectNotFound
		}
		return domain.ProjectExecutionSettings{}, err
	}
	settings, err := s.deps.Store.Reader().GetProjectExecutionSettings(ctx, projectID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return defaultProjectExecutionSettings(projectID), nil
	}
	return settings, err
}

func (s *Service) UpdateProjectExecutionSettings(ctx context.Context, request UpdateProjectExecutionSettingsRequest) (domain.ProjectExecutionSettings, error) {
	if s == nil || s.deps.Store == nil || !request.ProjectID.Valid() {
		return domain.ProjectExecutionSettings{}, fmt.Errorf("%w: invalid Project execution settings request", ErrInvalidCommand)
	}
	profile, err := domain.ParseAgentExecutionProfile(string(request.AgentExecutionProfile))
	if err != nil || !currentProjectExecutionProfile(profile) {
		return domain.ProjectExecutionSettings{}, fmt.Errorf("%w: invalid execution environment", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "update_project_execution_settings", request.ProjectID.String(), request.ExpectedVersion); err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "update_project_execution_settings", request.ProjectID.String(), request.ExpectedVersion, struct {
		Profile domain.AgentExecutionProfile
		Model   *domain.ModelIdentity
	}{profile, request.Model}, now)
	if err != nil {
		return domain.ProjectExecutionSettings{}, err
	}
	var result domain.ProjectExecutionSettings
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire projectExecutionSettingsWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			result, err = restoreProjectExecutionSettingsWire(wire)
			return err
		}
		project, err := tx.GetProject(ctx, request.ProjectID)
		if err != nil {
			if errors.Is(err, storecontract.ErrNotFound) {
				return ErrProjectNotFound
			}
			return err
		}
		if project.State() != domain.ProjectStateActive {
			return fmt.Errorf("%w: restore the Project before editing settings", ErrInvalidCommand)
		}
		if request.Model != nil {
			resolver := request.ResolveExecutionModel
			if resolver == nil {
				resolver = s.deps.ResolveExecutionModel
			}
			if resolver == nil {
				return fmt.Errorf("%w: execution model resolver is unavailable", ErrInvalidCommand)
			}
			binding, err := resolver(ctx, profile, request.Model)
			if err != nil {
				return fmt.Errorf("%w: resolve execution model: %v", ErrInvalidCommand, err)
			}
			if !binding.Configured() || binding.Record().ModelIdentity != *request.Model {
				return fmt.Errorf("%w: resolved model does not match the selected model", ErrInvalidCommand)
			}
		}
		if request.ExpectedVersion == 0 {
			if _, err := tx.GetProjectExecutionSettings(ctx, request.ProjectID); err == nil {
				return storecontract.ErrVersionConflict
			} else if !errors.Is(err, storecontract.ErrNotFound) {
				return err
			}
		} else {
			current, err := tx.GetProjectExecutionSettings(ctx, request.ProjectID)
			if err != nil || current.Version != request.ExpectedVersion {
				if errors.Is(err, storecontract.ErrNotFound) || err == nil {
					return storecontract.ErrVersionConflict
				}
				return err
			}
		}
		result = domain.ProjectExecutionSettings{ProjectID: request.ProjectID, Version: request.ExpectedVersion + 1, AgentExecutionProfile: profile, Model: request.Model, UpdatedAt: now}
		if err := result.Validate(); err != nil {
			return err
		}
		if err := tx.SaveProjectExecutionSettings(ctx, request.ExpectedVersion, result); err != nil {
			return err
		}
		response, err = jsonResponse(wireProjectExecutionSettings(result))
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func currentProjectExecutionProfile(profile domain.AgentExecutionProfile) bool {
	return profile == domain.AgentExecutionProfileTrustedLocal || profile == domain.AgentExecutionProfileIsolatedLocal
}
