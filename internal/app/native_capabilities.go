package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
)

type UpdateNativeCapabilitiesRequest struct {
	CommandMeta
	ProjectID          domain.ProjectID
	ExpectedVersion    uint64
	SkillPaths         []string
	DisabledSkillPaths []string
	BridgePath         string
	MCPConfigPath      string
	DisabledMCPServers []string
}

func (s *Service) GetNativeCapabilities(ctx context.Context, projectID domain.ProjectID) (nativecapabilities.Config, error) {
	if err := s.requireProjectStore(); err != nil {
		return nativecapabilities.Config{}, err
	}
	if !projectID.Valid() {
		return nativecapabilities.Config{}, fmt.Errorf("%w: invalid Project ID", ErrInvalidCommand)
	}
	if _, err := s.GetProjectByID(ctx, projectID); err != nil {
		return nativecapabilities.Config{}, err
	}
	if strings.TrimSpace(s.deps.DataRoot) == "" {
		return nativecapabilities.Config{}, fmt.Errorf("%w: data root is unavailable", ErrInvalidCommand)
	}
	return nativecapabilities.Read(s.deps.DataRoot, projectID.String())
}

func (s *Service) UpdateNativeCapabilities(ctx context.Context, request UpdateNativeCapabilitiesRequest) (nativecapabilities.Config, error) {
	if err := s.requireProjectStore(); err != nil {
		return nativecapabilities.Config{}, err
	}
	if !request.ProjectID.Valid() {
		return nativecapabilities.Config{}, fmt.Errorf("%w: invalid Project ID", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "update_native_capabilities", request.ProjectID.String(), request.ExpectedVersion); err != nil {
		return nativecapabilities.Config{}, err
	}
	project, err := s.GetProjectByID(ctx, request.ProjectID)
	if err != nil {
		return nativecapabilities.Config{}, err
	}
	if project.Project.State() != domain.ProjectStateActive {
		return nativecapabilities.Config{}, fmt.Errorf("%w: restore the Project before editing native capabilities", ErrInvalidCommand)
	}
	if strings.TrimSpace(s.deps.DataRoot) == "" {
		return nativecapabilities.Config{}, fmt.Errorf("%w: data root is unavailable", ErrInvalidCommand)
	}
	next := nativecapabilities.Config{
		SkillPaths: request.SkillPaths, DisabledSkillPaths: request.DisabledSkillPaths,
		BridgePath: request.BridgePath, MCPConfigPath: request.MCPConfigPath,
		DisabledMCPServers: request.DisabledMCPServers,
	}
	return nativecapabilities.Save(s.deps.DataRoot, request.ProjectID.String(), request.ExpectedVersion, next)
}

// AuthorizeNativeCapabilityVerification validates the Project boundary before
// the HTTP layer starts a temporary local verification process.
func (s *Service) AuthorizeNativeCapabilityVerification(ctx context.Context, meta CommandMeta, projectID domain.ProjectID) error {
	if err := s.requireProjectStore(); err != nil {
		return err
	}
	if !projectID.Valid() {
		return fmt.Errorf("%w: invalid Project ID", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, meta, "verify_native_capabilities", projectID.String(), 0); err != nil {
		return err
	}
	project, err := s.GetProjectByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Project.State() != domain.ProjectStateActive {
		return fmt.Errorf("%w: restore the Project before verifying native capabilities", ErrInvalidCommand)
	}
	return nil
}
