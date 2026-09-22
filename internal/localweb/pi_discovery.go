package localweb

import (
	"encoding/hex"
	"net/http"

	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/piinstall"
)

const piDiscoveryUnavailableReason = "Local execution Pi discovery is not configured"

type piDiscoveryView struct {
	ConfigurationAction string   `json:"configurationAction,omitempty"`
	State               string   `json:"state"`
	ExecutablePath      string   `json:"executablePath,omitempty"`
	Version             string   `json:"version,omitempty"`
	ExecutableSHA256    string   `json:"executableSha256,omitempty"`
	ReadyProviders      []string `json:"readyProviders"`
	NotReadyProviders   []string `json:"notReadyProviders"`
	Reason              string   `json:"reason,omitempty"`
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
	discovery := server.piDiscoveryOptions
	if installation.State != nil && installation.State.SelectionPresent {
		selection, selectionErr := server.piInstaller.ResolveSelection(request.Context())
		if selectionErr != nil {
			writePiInstallationError(writer, selectionErr)
			return
		}
		discovery.LookPath = func(string) (string, error) { return selection.ExecutablePath, nil }
	}
	result, err := pidiscovery.Discover(request.Context(), discovery)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view := piDiscoveryViewOf(result)
	if result.State == pidiscovery.StateUnconfigured && result.ExecutablePath != "" {
		view.ConfigurationAction = piinstall.ConfigurationCommand(result.ExecutablePath)
	}
	writeJSON(writer, http.StatusOK, view)
}
