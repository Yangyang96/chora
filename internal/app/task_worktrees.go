package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var ErrTaskWorktreeUnavailable = errors.New("Task worktree unavailable")

// executionRootForCharter returns the absolute host path where an Attempt for
// the charter's Task executes. A Room-bound Task resolves to its proven durable
// worktree; an unbound Task keeps the charter's logical WorkspaceRoot (the M1
// installed path).
func (s *Service) executionRootForCharter(ctx context.Context, charter domain.RunCharter) (string, error) {
	if _, err := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, charter.TaskID()); err == nil {
		if s.deps.TaskResourceWorkspaces == nil {
			return "", ErrTaskWorktreeUnavailable
		}
		return s.deps.TaskResourceWorkspaces.ResolveExecutionRoot(ctx, charter.TaskID())
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return "", err
	}

	if s.deps.TaskWorktrees == nil {
		return charter.WorkspaceRoot(), nil
	}
	task, err := s.deps.Store.Reader().GetTask(ctx, charter.TaskID())
	if err != nil {
		return "", err
	}
	resolved, err := s.deps.TaskWorktrees.ResolveExecutionRoot(ctx, task)
	if err != nil {
		return "", err
	}
	if resolved != "" {
		return resolved, nil
	}
	return charter.WorkspaceRoot(), nil
}

// ensureExistingTaskWorktree is a no-op for legacy and diagnostic Tasks that
// have no durable binding. A bound Task always fails closed when its worktree
// cannot be proven ready.
func (s *Service) ensureExistingTaskWorktree(ctx context.Context, taskID domain.TaskID) error {
	if _, err := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, taskID); err == nil {
		if s.deps.TaskResourceWorkspaces == nil {
			return ErrTaskWorktreeUnavailable
		}
		return s.deps.TaskResourceWorkspaces.Ensure(ctx, taskID)
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return err
	}

	if s.deps.TaskWorktrees == nil {
		return nil
	}
	if _, err := s.deps.Store.Reader().GetTaskWorktreeBinding(ctx, taskID); errors.Is(err, storecontract.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	binding, err := s.ensureTaskWorktree(ctx, taskID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTaskWorktreeUnavailable, err)
	}
	if binding.State() != domain.TaskWorktreeReady {
		return fmt.Errorf("%w: %s", ErrTaskWorktreeUnavailable, binding.State())
	}
	return nil
}

func (s *Service) ensureTaskWorktree(ctx context.Context, taskID domain.TaskID) (domain.TaskWorktreeBinding, error) {
	if s.deps.TaskWorktrees == nil {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: manager is not configured", ErrTaskWorktreeUnavailable)
	}
	unlock := s.candidateLocks.lock("task-worktree:" + taskID.String())
	defer unlock()

	binding, err := s.deps.Store.Reader().GetTaskWorktreeBinding(ctx, taskID)
	if err != nil {
		return domain.TaskWorktreeBinding{}, err
	}
	if binding.State() == domain.TaskWorktreeRecoveryRequired {
		retrying, transitionErr := binding.RetryProvisioning(s.deps.Clock.Now())
		if transitionErr != nil {
			return binding, transitionErr
		}
		if err := s.saveTaskWorktreeBinding(ctx, binding, retrying); err != nil {
			return binding, err
		}
		binding = retrying
	}
	if err := s.deps.TaskWorktrees.Ensure(ctx, binding); err != nil {
		failed, transitionErr := binding.RecoveryRequired("managed Git worktree could not be proven", s.deps.Clock.Now())
		if transitionErr == nil {
			if saveErr := s.saveTaskWorktreeBinding(ctx, binding, failed); saveErr == nil {
				binding = failed
			}
		}
		return binding, err
	}
	if binding.State() == domain.TaskWorktreeReady {
		return binding, nil
	}
	ready, err := binding.Ready(s.deps.Clock.Now())
	if err != nil {
		return binding, err
	}
	if err := s.saveTaskWorktreeBinding(ctx, binding, ready); err != nil {
		return binding, err
	}
	return ready, nil
}

func (s *Service) saveTaskWorktreeBinding(ctx context.Context, before, after domain.TaskWorktreeBinding) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, before.Version(), after)
	})
}

func (s *Service) recoverTaskWorktrees(ctx context.Context) error {
	if s.deps.TaskWorktrees == nil {
		return nil
	}
	candidates, err := s.deps.Store.Reader().ListTaskWorktreeRecoveryCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		_, _ = s.ensureTaskWorktree(ctx, candidate.TaskID())
	}
	return nil
}

// ResolveTaskResourceRoot exposes the proven existing execution directory for
// local external-tool handoff. It never prepares or silently selects a repository.
func (s *Service) ResolveTaskResourceRoot(ctx context.Context, taskID domain.TaskID) (string, error) {
	if _, err := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, taskID); err != nil {
		return "", err
	}
	if s.deps.TaskResourceWorkspaces == nil {
		return "", ErrTaskWorktreeUnavailable
	}
	// Handoff accepts the owned branch after delivery commits, while native
	// execution continues to require the frozen base through ResolveExecutionRoot.
	if resolver, ok := s.deps.TaskResourceWorkspaces.(interface {
		ResolveHandoffRoot(context.Context, domain.TaskID) (string, error)
	}); ok {
		return resolver.ResolveHandoffRoot(ctx, taskID)
	}
	return s.deps.TaskResourceWorkspaces.ResolveExecutionRoot(ctx, taskID)
}
