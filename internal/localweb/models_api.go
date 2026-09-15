package localweb

import (
	"context"
	"fmt"
	"net/http"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

// Model catalogs belong to the Runtime selected by the execution profile.
func (server *Server) getSupportedModels(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, nil)
		return
	}
	profile := request.URL.Query().Get("agentExecutionProfile")
	var catalog domain.ModelCatalog
	var err error
	switch profile {
	case "isolated_local":
		catalog, err = server.isolatedLocal.modelCatalog(request.Context())
	case "", "local_connected", "trusted_local":
		catalog, err = pathModelCatalog(request.Context(), server.piDiscoveryOptions)
	default:
		writeError(writer, http.StatusBadRequest, fmt.Errorf("unknown model execution profile"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("Runtime model capabilities unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, catalog)
}

func pathModelCatalog(ctx context.Context, options pidiscovery.Options) (domain.ModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := pidiscovery.Discover(ctx, options)
	if err != nil || result.State != pidiscovery.StateReady {
		return domain.ModelCatalog{}, fmt.Errorf("Pi Runtime is not ready")
	}
	source, err := agentpi.NewPathPiSource(agentpi.PathPiSourceParams{ExecutablePath: result.ExecutablePath, Version: result.Version, ExecutableSHA256: result.ExecutableSHA256})
	if err != nil {
		return domain.ModelCatalog{}, err
	}
	models, err := pidiscovery.DiscoverModelsForRuntime(ctx, source.ExecutablePath(), options.PiHome)
	if err != nil {
		return domain.ModelCatalog{}, err
	}
	// Recheck executable bytes after discovery; never bind a mixed snapshot.
	if verified, err := pidiscovery.Revalidate(ctx, result); err != nil || verified.State != pidiscovery.StateReady {
		return domain.ModelCatalog{}, fmt.Errorf("Pi Runtime identity changed during discovery")
	}
	return runtimeModelCatalog("path-pi", fmt.Sprintf("%x", source.SourceIdentity()), source.Version(), models)
}

func runtimeModelCatalog(agent, identity, version string, options []pidiscovery.ModelOption) (domain.ModelCatalog, error) {
	models := make([]domain.ModelIdentity, 0, len(options))
	for _, m := range options {
		models = append(models, domain.ModelIdentity{Provider: m.Provider, ModelID: m.ModelID})
	}
	return domain.NewModelCatalog(agent, identity, version, models)
}
