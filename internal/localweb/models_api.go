package localweb

import (
	"encoding/json"
	"net/http"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
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
	_ = json.NewEncoder(writer).Encode(struct {
		Models []agentpi.SupportedModel `json:"models"`
	}{Models: agentpi.SupportedModels()})
}
