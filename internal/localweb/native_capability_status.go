package localweb

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/nativecapabilities"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

func (server *Server) verifyNativeCapabilities(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	if err := server.service.AuthorizeNativeCapabilityVerification(request.Context(), requestCommandMeta(request, "verify-native-capabilities"), id); err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	if !server.pathPiEnabled {
		writeError(writer, http.StatusConflict, errors.New("native capability verification requires Local execution"))
		return
	}
	config, err := server.service.GetNativeCapabilities(request.Context(), id)
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	discovery, err := pidiscovery.Discover(request.Context(), server.piDiscoveryOptions)
	if err != nil || discovery.State != pidiscovery.StateReady {
		writeError(writer, http.StatusServiceUnavailable, errors.New("Pi is unavailable"))
		return
	}
	home, err := pidiscovery.ResolvePiHome(server.piDiscoveryOptions.PiHome)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("Pi configuration directory unavailable"))
		return
	}
	observation, err := nativecapabilities.Verify(request.Context(), server.runtimeRoot, id.String(), discovery.ExecutablePath, home, server.runtimeRoot, config)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(writer, http.StatusOK, observation)
}

func (server *Server) getNativeCapabilityStatus(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	config, err := server.service.GetNativeCapabilities(request.Context(), id)
	if err != nil {
		writeNativeCapabilitiesError(writer, err)
		return
	}
	view := struct {
		Inventory    *nativecapabilities.Inventory    `json:"inventory,omitempty"`
		Observations []nativecapabilities.Observation `json:"observations"`
		Warning      string                           `json:"warning,omitempty"`
	}{Observations: []nativecapabilities.Observation{}}
	discovery, err := pidiscovery.Discover(request.Context(), server.piDiscoveryOptions)
	if err != nil || discovery.State != pidiscovery.StateReady {
		view.Warning = "Pi is unavailable. Configure the Local execution runtime first."
	} else {
		home, e := pidiscovery.ResolvePiHome(server.piDiscoveryOptions.PiHome)
		if e == nil {
			inventory, e := nativecapabilities.Inspect(request.Context(), discovery.ExecutablePath, home, server.runtimeRoot, config)
			if e == nil {
				inventory.ExtensionPaths = nil
				inventory.ConfigurationDigest = ""
				inventory.ResourceDigest = ""
				view.Inventory = &inventory
			} else {
				view.Warning = e.Error()
			}
		} else {
			view.Warning = "Pi configuration directory is unavailable."
		}
	}
	observations, _ := nativecapabilities.Observations(filepath.Join(server.runtimeRoot, "native-capabilities"), id.String())
	for _, observation := range observations {
		attemptID, e := domain.ParseAttemptID(observation.AttemptID)
		if e != nil {
			continue
		}
		attempt, e := server.store.Reader().GetAttempt(request.Context(), attemptID)
		if e != nil {
			continue
		}
		run, e := server.store.Reader().GetRun(request.Context(), attempt.RunID())
		if e != nil {
			continue
		}
		task, e := server.store.Reader().GetTask(request.Context(), run.TaskID())
		if e != nil {
			continue
		}
		room, e := server.store.Reader().GetRoom(request.Context(), task.RoomID())
		if e != nil || room.ProjectID() != id {
			continue
		}
		observation.Running = observation.Running && attempt.State() == domain.AttemptStateRunning
		view.Observations = append(view.Observations, observation)
	}
	writeJSON(writer, http.StatusOK, view)
}
