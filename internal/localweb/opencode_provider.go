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
	provider, err := opencoderun.New(ctx, config)
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	supervisor, err := trustedhost.New(provider.SupervisorConfig(runtimeRoot))
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	return RuntimeProviderRegistration{Provider: provider, Supervisors: map[string]execution.ProcessSupervisor{domain.TrustedHostExecutionProvider: supervisor}}, nil
}
