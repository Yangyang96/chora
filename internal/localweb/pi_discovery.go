package localweb

import (
	"encoding/hex"
	"net/http"

	"github.com/Yangyang96/chora/internal/pidiscovery"
)

const piDiscoveryUnavailableReason = "Local Connected Pi discovery is not configured"

type piDiscoveryView struct {
	State             string   `json:"state"`
	ExecutablePath    string   `json:"executablePath,omitempty"`
	Version           string   `json:"version,omitempty"`
	ExecutableSHA256  string   `json:"executableSha256,omitempty"`
	ReadyProviders    []string `json:"readyProviders"`
	NotReadyProviders []string `json:"notReadyProviders"`
	Reason            string   `json:"reason,omitempty"`
}

func piDiscoveryViewOf(result pidiscovery.Result) piDiscoveryView {
	view := piDiscoveryView{
		State:             string(result.State),
		ExecutablePath:    result.ExecutablePath,
		Version:           result.Version,
		ReadyProviders:    result.ReadyProviders,
		NotReadyProviders: result.NotReadyProviders,
		Reason:            result.Reason,
	}
	if result.ExecutableSHA256 != ([32]byte{}) {
		view.ExecutableSHA256 = hex.EncodeToString(result.ExecutableSHA256[:])
	}
	if view.ReadyProviders == nil {
		view.ReadyProviders = []string{}
	}
	if view.NotReadyProviders == nil {
		view.NotReadyProviders = []string{}
	}
	return view
}

func (server *Server) getPiDiscovery(writer http.ResponseWriter, request *http.Request) {
	if !server.pathPiEnabled {
		writeJSON(writer, http.StatusOK, piDiscoveryView{State: "unavailable", Reason: piDiscoveryUnavailableReason, ReadyProviders: []string{}, NotReadyProviders: []string{}})
		return
	}
	if server.piInstallationBlocked {
		writeJSON(writer, http.StatusOK, piDiscoveryView{State: "drifted", Reason: "The Chora-selected Pi installation is missing or invalid.", ReadyProviders: []string{}, NotReadyProviders: []string{}})
		return
	}
	installation, err := server.inspectPiInstallation(request.Context())
	if err != nil {
		writePiInstallationError(writer, err)
		return
	}
	if installation.Available && installation.State != nil && (installation.RestartRequired || installation.State.Code == "installing" || installation.State.Code == "drifted") {
		state, reason := "restart_required", "Restart Chora to activate the selected Pi installation."
		if installation.State.Code == "installing" {
			state, reason = "installing", "Pi installation is in progress."
		}
		if installation.State.Code == "drifted" {
			state, reason = "drifted", "The Chora-selected Pi installation is missing or invalid."
		}
		writeJSON(writer, http.StatusOK, piDiscoveryView{State: state, Reason: reason, ReadyProviders: []string{}, NotReadyProviders: []string{}})
		return
	}
	result, err := pidiscovery.Discover(request.Context(), server.piDiscoveryOptions)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, piDiscoveryViewOf(result))
}
