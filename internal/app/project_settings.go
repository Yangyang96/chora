package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type UpdateProjectSettingsRequest struct {
	CommandMeta
	ProjectID            domain.ProjectID
	ExpectedVersion      uint64
	WritableFiles        []string
	WritableDirectories  []string
	VerificationCommands []domain.ProjectVerificationCommand
	NoChecks             bool
}

func (s *Service) GetProjectSettings(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSettings, error) {
	if err := s.requireProjectStore(); err != nil {
		return domain.ProjectSettings{}, err
	}
	if !projectID.Valid() {
		return domain.ProjectSettings{}, fmt.Errorf("%w: invalid Project ID", ErrInvalidCommand)
	}
	if _, err := s.deps.Store.Reader().GetProject(ctx, projectID); err != nil {
		if errors.Is(err, storecontract.ErrNotFound) {
			return domain.ProjectSettings{}, ErrProjectNotFound
		}
		return domain.ProjectSettings{}, err
	}
	return s.deps.Store.Reader().GetProjectSettings(ctx, projectID)
}

func (s *Service) UpdateProjectSettings(ctx context.Context, request UpdateProjectSettingsRequest) (domain.ProjectSettings, error) {
	if err := s.requireProjectStore(); err != nil {
		return domain.ProjectSettings{}, err
	}
	if !request.ProjectID.Valid() {
		return domain.ProjectSettings{}, fmt.Errorf("%w: invalid Project ID", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "update_project_settings", request.ProjectID.String(), request.ExpectedVersion); err != nil {
		return domain.ProjectSettings{}, err
	}
	params := domain.ProjectSettingsParams{
		ProjectID: request.ProjectID, WritableFiles: request.WritableFiles,
		WritableDirectories: request.WritableDirectories, VerificationCommands: request.VerificationCommands,
		NoChecks: request.NoChecks, UpdatedAt: s.deps.Clock.Now(),
	}
	var next domain.ProjectSettings
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
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
		if request.ExpectedVersion == 0 {
			if _, err := tx.GetProjectSettings(ctx, request.ProjectID); err == nil {
				return storecontract.ErrVersionConflict
			} else if !errors.Is(err, storecontract.ErrNotFound) {
				return err
			}
			next, err = domain.NewProjectSettings(params)
			if err != nil {
				return err
			}
			return tx.SaveProjectSettingsCAS(ctx, 0, next)
		}
		current, err := tx.GetProjectSettings(ctx, request.ProjectID)
		if err != nil {
			if errors.Is(err, storecontract.ErrNotFound) {
				return storecontract.ErrVersionConflict
			}
			return err
		}
		next, err = current.Update(params, request.ExpectedVersion)
		if err != nil {
			if current.Version() != request.ExpectedVersion {
				return storecontract.ErrVersionConflict
			}
			return err
		}
		return tx.SaveProjectSettingsCAS(ctx, request.ExpectedVersion, next)
	})
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	return next, nil
}
