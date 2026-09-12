package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// SpecCodingEnvelope is the Spec Coding envelope surface the Service needs to
// declare, restore, and accept a Real Spec Coding Task. Both the installed M1
// envelope and the per-Room Local Connected BoundEnvelope satisfy it.
type SpecCodingEnvelope interface {
	Repository() speccoding.RepositoryIdentity
	CapabilityEnvelope() domain.CapabilityEnvelope
	MatchesRepositoryRoot(string) bool
	Declare(speccoding.UserTaskDeclaration) (speccoding.DeclaredUserTask, error)
	RestoreDeclaredUserTask([]byte) (speccoding.DeclaredUserTask, error)
	Accept(speccoding.DeclaredUserTask, speccoding.UserTaskPlan, domain.ContextSnapshotID, string) (speccoding.CoreContract, error)
}

// SpecCodingEnvelopeResolver selects the Spec Coding envelope for a Room. A
// Room without a RepositoryBinding resolves to the installed M1 envelope.
type SpecCodingEnvelopeResolver interface {
	ResolveSpecCodingEnvelope(context.Context, domain.Room) (SpecCodingEnvelope, error)
}

// IsolatedResourceSpecCodingEnvelopeResolver is an optional extension for the
// frozen Isolated Local resource boundary. Callers must fail closed when an
// isolated Task is restored without this resolver.
type IsolatedResourceSpecCodingEnvelopeResolver interface {
	ResolveIsolatedResourceSpecCodingEnvelope(context.Context, domain.Room, domain.TaskResourceSnapshot) (SpecCodingEnvelope, error)
}

// resolveSpecCodingEnvelope returns the envelope for a Room, preferring the
// per-Room resolver and falling back to the installed M1 envelope. The installed
// path stays byte-identical when no resolver is configured.
func (s *Service) resolveSpecCodingEnvelope(ctx context.Context, room domain.Room) (SpecCodingEnvelope, error) {
	if s.deps.SpecCodingEnvelopeResolver != nil {
		return s.deps.SpecCodingEnvelopeResolver.ResolveSpecCodingEnvelope(ctx, room)
	}
	if s.deps.InstalledSpecCodingEnvelope == nil {
		return nil, fmt.Errorf("%w: installed envelope is not configured", speccoding.ErrInvalidInstalledEnvelope)
	}
	return s.deps.InstalledSpecCodingEnvelope, nil
}

// Resolve existing Tasks against their persisted authority, including when the
// caller is inside a transaction. No lookup substitutes a new Room HEAD.
func (s *Service) resolveTaskSpecCodingEnvelope(ctx context.Context, reader storecontract.Reader, room domain.Room, taskID domain.TaskID) (SpecCodingEnvelope, error) {
	if record, err := reader.GetTaskResourceSnapshot(ctx, taskID); err == nil {
		var snapshot domain.TaskResourceSnapshot
		if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
			return nil, err
		}
		preference, err := reader.GetTaskAgentExecutionProfilePreference(ctx, taskID)
		if err != nil {
			return nil, err
		}
		return s.resolveResourceSpecCodingEnvelope(ctx, room, snapshot, preference.Profile())
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return nil, err
	}

	if resolver, ok := s.deps.SpecCodingEnvelopeResolver.(interface {
		ResolveTaskSpecCodingEnvelope(context.Context, domain.Room, domain.TaskWorktreeBinding) (SpecCodingEnvelope, error)
	}); ok {
		base, err := reader.GetTaskWorktreeBinding(ctx, taskID)
		if err != nil {
			return nil, err
		}
		return resolver.ResolveTaskSpecCodingEnvelope(ctx, room, base)
	}
	return s.resolveSpecCodingEnvelope(ctx, room)
}

func (s *Service) resolveResourceSpecCodingEnvelope(ctx context.Context, room domain.Room, snapshot domain.TaskResourceSnapshot, profile domain.AgentExecutionProfile) (SpecCodingEnvelope, error) {
	if profile == domain.AgentExecutionProfileIsolatedLocal {
		resolver, ok := s.deps.SpecCodingEnvelopeResolver.(IsolatedResourceSpecCodingEnvelopeResolver)
		if !ok {
			return nil, fmt.Errorf("%w: isolated task resource envelope is unavailable", speccoding.ErrInvalidInstalledEnvelope)
		}
		return resolver.ResolveIsolatedResourceSpecCodingEnvelope(ctx, room, snapshot)
	}
	resolver, ok := s.deps.SpecCodingEnvelopeResolver.(interface {
		ResolveResourceSpecCodingEnvelope(context.Context, domain.Room, domain.TaskResourceSnapshot) (SpecCodingEnvelope, error)
	})
	if !ok {
		return nil, fmt.Errorf("%w: task resource envelope is unavailable", speccoding.ErrInvalidInstalledEnvelope)
	}
	return resolver.ResolveResourceSpecCodingEnvelope(ctx, room, snapshot)
}
