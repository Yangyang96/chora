package localweb

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// RuntimeProviderRegistration pairs a protocol implementation with only its
// explicitly validated launch supervisors. No arbitrary executable fallback.
type RuntimeProviderRegistration struct {
	Provider    execution.Provider
	Supervisors map[string]execution.ProcessSupervisor
}

func registerRuntimeProviders(registry *adapterRegistry, router *routingSupervisor, registrations []RuntimeProviderRegistration) error {
	for _, registration := range registrations {
		provider := registration.Provider
		if provider == nil {
			return execution.ErrInvalidRuntimeConfig
		}
		descriptor := provider.Describe()
		if err := descriptor.Validate(); err != nil {
			return err
		}
		if provider.ID() == "codex" || provider.ID() != descriptor.ID {
			return execution.ErrInvalidRuntimeConfig
		}
		if _, err := registry.Get(provider.ID()); err == nil {
			return fmt.Errorf("%w: duplicate runtime", execution.ErrInvalidRuntimeConfig)
		}
		if len(registration.Supervisors) == 0 {
			return execution.ErrInvalidRuntimeConfig
		}
		for environment, supervisor := range registration.Supervisors {
			if supervisor == nil || !slices.Contains(descriptor.Environments, environment) {
				return execution.ErrUnsupportedCapability
			}
			target, err := execution.NewExecutionTarget(provider.ID(), environment)
			if err != nil {
				return err
			}
			if err := router.registerTarget(target, supervisor); err != nil {
				return err
			}
		}
		if err := registry.providers.Register(provider); err != nil {
			return err
		}
	}
	return nil
}
func restoreProviderRoutes(ctx context.Context, reader storecontract.Reader, registry *adapterRegistry, router *routingSupervisor) error {
	candidates, err := reader.ListStartupCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Attempt == nil || candidate.Attempt.AgentExecutionProfileBinding().RuntimeBindingJSON() == "" {
			continue
		}
		session, err := reader.GetRuntimeSessionForAttempt(ctx, candidate.Attempt.ID())
		if errors.Is(err, storecontract.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !session.FinalizedAt.IsZero() {
			continue
		}
		binding := candidate.Attempt.AgentExecutionProfileBinding()
		if binding.RuntimeID() != session.AdapterID {
			return execution.ErrInvalidRuntimeConfig
		}
		// Missing registration is not proof of death. App preserves recovery_required.
		if _, err := registry.Get(session.AdapterID); err != nil {
			continue
		}
		target, err := execution.NewExecutionTarget(session.AdapterID, binding.ExecutionProvider())
		if err != nil {
			return err
		}
		if !router.hasTarget(target) {
			continue
		}
		if err := router.RestoreTarget(ctx, target, execution.ProcessIdentity{Value: session.ProcessIdentity}, execution.LaunchToken{Value: session.LaunchToken}); err != nil && !errors.Is(err, errRuntimeOwnershipUnproven) {
			return err
		}
	}
	return nil
}
