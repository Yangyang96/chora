package app

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
)

func (s *Service) SCMAvailability(ctx context.Context, taskID domain.TaskID) (SCMAvailability, error) {
	if s == nil || s.deps.SCM == nil {
		return SCMAvailability{Configured: false, Reason: UnconfiguredSCMReason}, nil
	}
	return s.deps.SCM.Availability(ctx, taskID)
}
