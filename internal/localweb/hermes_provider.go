package localweb

import (
	"context"

	"github.com/Yangyang96/chora/internal/agent/hermes"
)

// NewHermesRuntimeProvider is the explicit composition boundary. Refusal creates
// no supervisor, directory or route. Pi defaults remain unchanged. There is no
// production Hermes launch path until native worker custody is established.
func NewHermesRuntimeProvider(ctx context.Context, config hermes.Config, runtimeRoot string) (RuntimeProviderRegistration, error) {
	_, err := hermes.New(ctx, config)
	return RuntimeProviderRegistration{}, err
}
