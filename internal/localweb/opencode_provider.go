package localweb

import (
	"context"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// NewOpenCodeRuntimeProvider composes the explicitly selected installation and
// its trusted-host supervisor. Current native qualification failures propagate
// before registration, binding, persistence, or process launch.
func NewOpenCodeRuntimeProvider(ctx context.Context, config opencoderun.Config, runtimeRoot string) (RuntimeProviderRegistration, error) {
	return NewOpenCodeRuntimeProviderWithProtocolReviews(ctx, config, runtimeRoot, nil)
}

// NewOpenCodeRuntimeProviderWithProtocolReviews consumes only a host-owned trust
// service and propagates native safety rejection before route construction.
func NewOpenCodeRuntimeProviderWithProtocolReviews(ctx context.Context, config opencoderun.Config, runtimeRoot string, reviews *opencoderun.ProtocolReviews) (RuntimeProviderRegistration, error) {
	provider, err := opencoderun.NewReviewed(ctx, config, reviews)
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	supervisor, err := trustedhost.New(provider.SupervisorConfig(runtimeRoot))
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	return RuntimeProviderRegistration{Provider: provider, Supervisors: map[string]execution.ProcessSupervisor{domain.TrustedHostExecutionProvider: supervisor}}, nil
}
