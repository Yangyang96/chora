package localweb

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/piinstall"
)

type piInstallationView struct {
	Available       bool             `json:"available"`
	State           *piinstall.State `json:"state,omitempty"`
	RestartRequired bool             `json:"restartRequired"`
	Active          bool             `json:"active"`
}

type piReadinessProbe struct{ options pidiscovery.Options }

func (p piReadinessProbe) Probe(ctx context.Context, executable string) (piinstall.Readiness, error) {
	options := p.options
	options.LookPath = func(name string) (string, error) {
		if name != "pi" {
			return "", errors.New("only the selected Pi executable is available")
		}
		return executable, nil
	}
	result, err := pidiscovery.Discover(ctx, options)
	if err != nil {
		return piinstall.Readiness{}, err
	}
	return piinstall.Readiness{Configured: result.State == pidiscovery.StateReady, ReadyProviders: append([]string(nil), result.ReadyProviders...)}, nil
}

func preparePiInstallation(ctx context.Context, dataRoot string, options pidiscovery.Options) (piinstall.Installer, piinstall.Selection, pidiscovery.Options, error) {
	installer, err := piinstall.New(piinstall.Config{DataRoot: dataRoot, ReadinessProbe: piReadinessProbe{options: options}})
	if err != nil {
		return nil, piinstall.Selection{}, options, err
	}
	state, err := installer.Inspect(ctx)
	if err != nil {
		return nil, piinstall.Selection{}, options, err
	}
	selection, selectionErr := installer.ResolveSelection(ctx)
	if selectionErr != nil {
		if state.Code != piinstall.StateInstalled && !state.SelectionPresent && managedSelectionRecordAbsent(dataRoot) {
			return installer, piinstall.Selection{}, options, nil
		}
		return installer, piinstall.Selection{}, options, errors.New("managed Pi selection is missing or invalid")
	}
	bootSelection := selection
	managed := options
	managed.LookPath = func(name string) (string, error) {
		if name != "pi" {
			return "", errors.New("only Pi is available from the managed selection")
		}
		current, err := installer.ResolveSelection(context.Background())
		if err != nil || current != bootSelection {
			return "", errors.New("managed Pi selection changed after startup")
		}
		return current.ExecutablePath, nil
	}
	return installer, selection, managed, nil
}

func managedSelectionRecordAbsent(dataRoot string) bool {
	root, err := filepath.Abs(filepath.Clean(dataRoot))
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	_, err = os.Lstat(filepath.Join(root, "pi", "selection.json"))
	return os.IsNotExist(err)
}

func (server *Server) piInstallationAvailable() bool {
	return server.pathPiEnabled && server.piInstaller != nil
}

func (server *Server) inspectPiInstallation(ctx context.Context) (piInstallationView, error) {
	view := piInstallationView{Available: server.piInstallationAvailable()}
	if !view.Available {
		return view, nil
	}
	state, err := server.piInstaller.Inspect(ctx)
	if err != nil {
		return view, err
	}
	view.State = &state
	if !state.SelectionPresent {
		return view, nil
	}
	selection, err := server.piInstaller.ResolveSelection(ctx)
	if err != nil {
		state.Code, state.Reason, state.Message = piinstall.StateDrifted, piinstall.ReasonSelection, "managed Pi selection failed identity validation"
		state.Configured = false
		view.State = &state
		return view, nil
	}
	discovery := server.piDiscoveryOptions
	discovery.LookPath = func(string) (string, error) { return selection.ExecutablePath, nil }
	result, discoveryErr := pidiscovery.Discover(ctx, discovery)
	identityExact := discoveryErr == nil &&
		result.ExecutablePath == selection.ExecutablePath && result.Version == selection.PiVersion &&
		hex.EncodeToString(result.ExecutableSHA256[:]) == selection.ExecutableSHA256
	configured := identityExact && result.State == pidiscovery.StateReady && len(result.ReadyProviders) > 0
	state.Configured = configured
	if !configured {
		state.ConfigurationAction = piinstall.ConfigurationCommand(selection.ExecutablePath)
	}
	view.State = &state
	composedExact := identityExact && server.piStatus.Enabled && selection == server.piInstalledSelection
	view.Active = configured && composedExact
	view.RestartRequired = state.Code == piinstall.StateInstalled && !composedExact
	return view, nil
}

func (server *Server) getPiInstallation(writer http.ResponseWriter, request *http.Request) {
	view, err := server.inspectPiInstallation(request.Context())
	if err != nil {
		writePiInstallationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) installPi(writer http.ResponseWriter, request *http.Request) {
	if !server.piInstallationAvailable() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "pi_installation_unavailable"})
		return
	}
	var input struct {
		ExpectedStateVersion *uint64 `json:"expectedStateVersion"`
	}
	if err := decodeJSON(request, &input); err != nil || input.ExpectedStateVersion == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_pi_installation_request"})
		return
	}
	meta, ok := requestCommandMetaForProduct(writer, request, "install-pi", server.product)
	if !ok {
		return
	}
	ctx, cancel := serverLinkedContext(request.Context(), server.ctx)
	defer cancel()
	outcome, err := server.piInstaller.Install(ctx, piinstall.InstallRequest{IdempotencyKey: meta.IdempotencyKey, ExpectedStateVersion: *input.ExpectedStateVersion})
	if err != nil {
		writePiInstallationError(writer, err)
		return
	}
	view, inspectErr := server.inspectPiInstallation(request.Context())
	if inspectErr != nil {
		writePiInstallationError(writer, inspectErr)
		return
	}
	if view.State == nil {
		view.State = &outcome.State
	}
	if outcome.RestartRequired && !view.Active {
		view.RestartRequired = true
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) cancelPiInstallation(writer http.ResponseWriter, request *http.Request) {
	if !server.piInstallationAvailable() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "pi_installation_unavailable"})
		return
	}
	var input struct {
		OperationID string `json:"operationId"`
	}
	if err := decodeJSON(request, &input); err != nil || input.OperationID == "" || strings.TrimSpace(input.OperationID) != input.OperationID {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_pi_cancellation_request"})
		return
	}
	if _, ok := requestCommandMetaForProduct(writer, request, "cancel-pi-installation", server.product); !ok {
		return
	}
	ctx, cancel := serverLinkedContext(request.Context(), server.ctx)
	defer cancel()
	if err := server.piInstaller.Cancel(ctx, input.OperationID); err != nil {
		writePiInstallationError(writer, err)
		return
	}
	view, err := server.inspectPiInstallation(request.Context())
	if err != nil {
		writePiInstallationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func serverLinkedContext(request, lifetime context.Context) (context.Context, context.CancelFunc) {
	if lifetime == nil {
		lifetime = context.Background()
	}
	ctx, cancel := context.WithCancel(request)
	stop := context.AfterFunc(lifetime, cancel)
	return ctx, func() { stop(); cancel() }
}

func writePiInstallationError(writer http.ResponseWriter, err error) {
	reason := piinstall.ReasonUnexpected
	status := http.StatusInternalServerError
	var installErr *piinstall.Error
	if errors.As(err, &installErr) {
		reason = installErr.Reason
		if reason == piinstall.ReasonNone {
			reason = piinstall.ReasonUnexpected
		}
		switch reason {
		case piinstall.ReasonStateConflict, piinstall.ReasonInstallBusy, piinstall.ReasonCancelled:
			status = http.StatusConflict
		case piinstall.ReasonPrerequisite:
			status = http.StatusUnprocessableEntity
		}
	}
	writeJSON(writer, status, map[string]string{"error": string(reason)})
}
