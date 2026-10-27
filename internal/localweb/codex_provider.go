package localweb

import (
	"context"

	"github.com/Yangyang96/chora/internal/agent/codexexec"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// NewCodexRuntimeProvider composes an explicitly selected native installation
// with its exact supervisor. Callers supply this registration through Options;
// it does not change Pi defaults or enable a runtime from ambient discovery.
// Settings persistence and browser configuration belong to the product layer.
func NewCodexRuntimeProvider(ctx context.Context, config codexexec.Config, runtimeRoot string) (RuntimeProviderRegistration, error) {
	provider, err := codexexec.New(ctx, config)
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	supervisor, err := trustedhost.New(provider.SupervisorConfig(runtimeRoot))
	if err != nil {
		return RuntimeProviderRegistration{}, err
	}
	return RuntimeProviderRegistration{Provider: provider, Supervisors: map[string]execution.ProcessSupervisor{domain.TrustedHostExecutionProvider: supervisor}}, nil
}
