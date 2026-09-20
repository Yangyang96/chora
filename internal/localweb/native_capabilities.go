package localweb

import (
	"errors"
	"net/http"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
)

const nativeCapabilitiesRequestLimit = 64 << 10

func (server *Server) getNativeCapabilities(writer http.ResponseWriter, request *http.Request) {
	projectID, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	config, err := server.service.GetNativeCapabilities(request.Context(), projectID)
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, config)
}

func (server *Server) putNativeCapabilities(writer http.ResponseWriter, request *http.Request) {
	projectID, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, nativeCapabilitiesRequestLimit)
	var input nativecapabilities.Config
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	config, err := server.service.UpdateNativeCapabilities(request.Context(), app.UpdateNativeCapabilitiesRequest{
		CommandMeta: requestCommandMeta(request, "put-native-capabilities"), ProjectID: projectID,
		ExpectedVersion: input.Version, SkillPaths: input.SkillPaths, DisabledSkillPaths: input.DisabledSkillPaths,
		BridgePath: input.BridgePath, MCPConfigPath: input.MCPConfigPath, DisabledMCPServers: input.DisabledMCPServers,
	})
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, config)
}

func writeNativeCapabilitiesError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nativecapabilities.ErrConflict):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, nativecapabilities.ErrInvalidConfig):
		writeError(writer, http.StatusBadRequest, err)
	default:
		writeProjectError(writer, err)
	}
}
