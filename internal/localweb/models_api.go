package localweb

import (
	"encoding/json"
	"fmt"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"net/http"
)

// getSupportedModels exposes the immutable managed model catalog. Execution
// profiles are intentionally absent: selecting a profile cannot select or
// mutate a model.
func (server *Server) getSupportedModels(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, nil)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	options, err := pidiscovery.DiscoverModels(server.piDiscoveryOptions.PiHome)
	if err != nil || len(options) == 0 {
		writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Runtime model capabilities unavailable"))
		return
	}
	models := make([]agentpi.SupportedModel, 0, len(options))
	for _, option := range options {
		models = append(models, agentpi.SupportedModel{Provider: option.Provider, ModelID: option.ModelID})
	}
	_ = json.NewEncoder(writer).Encode(agentpi.NewSupportedModelCatalog("pi", server.piDiscoveryOptions.PiHome, pidiscovery.MinimumVersion, models))
}
