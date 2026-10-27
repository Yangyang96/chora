package agent

import (
	"fmt"
	"sync"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// Registry is explicit composition. It never discovers executables or falls
// back to another runtime. Registration is immutable for each runtime ID.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]execution.Provider
}

func NewRegistry() *Registry { return &Registry{providers: make(map[string]execution.Provider)} }
func (r *Registry) Register(provider execution.Provider) error {
	if provider == nil {
		return execution.ErrInvalidRuntimeConfig
	}
	descriptor := provider.Describe()
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if descriptor.ID != provider.ID() || descriptor.ResumeMode != provider.Capabilities().ResumeMode {
		return execution.ErrInvalidRuntimeConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]execution.Provider)
	}
	if _, exists := r.providers[provider.ID()]; exists {
		return fmt.Errorf("%w: duplicate runtime %s", execution.ErrInvalidRuntimeConfig, provider.ID())
	}
	r.providers[provider.ID()] = provider
	return nil
}
func (r *Registry) Get(id string) (execution.AgentAdapter, error) { return r.Resolve(id) }
func (r *Registry) Resolve(id string) (execution.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider := r.providers[id]
	if provider == nil || provider.ID() != id {
		return nil, fmt.Errorf("%w: %s", execution.ErrUnknownRuntime, id)
	}
	return provider, nil
}
func (r *Registry) Describe(id string) (execution.ProviderDescriptor, error) {
	provider, err := r.Resolve(id)
	if err != nil {
		return execution.ProviderDescriptor{}, err
	}
	return provider.Describe().Clone(), nil
}

// Bind freezes an inspected selection. Executable and configuration identities
// must come from the caller's qualified probe, not a guessed default.
func (r *Registry) Bind(id string, binding domain.RuntimeBindingV1, taskKind string, required ...string) (domain.AgentExecutionProfileBinding, error) {
	provider, err := r.Resolve(id)
	if err != nil {
		return domain.AgentExecutionProfileBinding{}, err
	}
	descriptor := provider.Describe()
	if binding.RuntimeID != id || binding.ProtocolVersion != descriptor.ProtocolVersion || binding.CapabilityDigest != fmt.Sprintf("%x", descriptor.Digest()) {
		return domain.AgentExecutionProfileBinding{}, execution.ErrInvalidRuntimeConfig
	}
	if err := descriptor.Require(binding.ExecutionProvider, taskKind, required...); err != nil {
		return domain.AgentExecutionProfileBinding{}, err
	}
	return domain.NewRuntimeProfileBinding(binding)
}
