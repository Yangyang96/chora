package localweb

import (
	"context"
	"errors"
	"net/http"
	"strings"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type trustedLocalAcknowledgementView struct {
	PolicyVersion  string `json:"policyVersion"`
	ActorID        string `json:"actorId"`
	SessionID      string `json:"sessionId"`
	AcknowledgedAt string `json:"acknowledgedAt"`
	Replayed       bool   `json:"replayed"`
}

type currentTrustedLocalAcknowledgementView struct {
	Acknowledged   bool   `json:"acknowledged"`
	PolicyVersion  string `json:"policyVersion"`
	AcknowledgedAt string `json:"acknowledgedAt,omitempty"`
}

func (server *Server) getCurrentTrustedLocalAcknowledgement(writer http.ResponseWriter, request *http.Request) {
	acknowledgement, err := server.store.Reader().GetTrustedLocalAcknowledgement(request.Context(), localActor, domain.TrustedLocalDisclosurePolicy)
	if errors.Is(err, storecontract.ErrNotFound) {
		writeJSON(writer, http.StatusOK, currentTrustedLocalAcknowledgementView{PolicyVersion: domain.TrustedLocalDisclosurePolicy})
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("Trusted Local acknowledgement state is unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, currentTrustedLocalAcknowledgementView{
		Acknowledged: true, PolicyVersion: acknowledgement.PolicyVersion(),
		AcknowledgedAt: acknowledgement.AcknowledgedAt().Format(timeFormatRFC3339Nano),
	})
}

func (server *Server) acknowledgeTrustedLocal(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		PolicyVersion string `json:"policyVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "acknowledge-trusted-local")
	if !ok {
		return
	}
	result, err := server.service.AcknowledgeTrustedLocal(request.Context(), app.AcknowledgeTrustedLocalRequest{
		CommandMeta: meta, PolicyVersion: strings.TrimSpace(input.PolicyVersion),
	})
	if err != nil {
		writeAgentExecutionCommandError(writer, err)
		return
	}
	acknowledgement := result.Acknowledgement
	writeJSON(writer, http.StatusOK, trustedLocalAcknowledgementView{
		PolicyVersion: acknowledgement.PolicyVersion(), ActorID: acknowledgement.ActorID(), SessionID: acknowledgement.SessionID(),
		AcknowledgedAt: acknowledgement.AcknowledgedAt().Format(timeFormatRFC3339Nano), Replayed: result.Replayed,
	})
}

func (server *Server) switchAgentExecutionProfile(writer http.ResponseWriter, request *http.Request) {
	runID, err := domain.ParseRunID(request.PathValue("runID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		Profile         string  `json:"profile"`
		Reason          string  `json:"reason"`
		ExpectedVersion *uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.ExpectedVersion == nil {
		writeError(writer, http.StatusBadRequest, errors.New("agent execution profile expectedVersion is required"))
		return
	}
	profile, err := domain.ParseAgentExecutionProfile(strings.TrimSpace(input.Profile))
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("unsupported agent execution profile"))
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "switch-agent-execution-profile")
	if !ok {
		return
	}
	binding, err := domain.NewAgentExecutionProfileBinding(profile)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("unsupported agent execution profile"))
		return
	}
	if !server.requireAgentExecutionRoute(writer, request, binding) {
		return
	}
	prepared, err := server.prepareAndBindManagedAttempt(request.Context(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareProfileSwitch(prepareCtx, app.PrepareProfileSwitchRequest{
			CommandMeta: meta, RunID: runID, ExpectedVersion: *input.ExpectedVersion, Profile: profile, Reason: input.Reason,
		})
	})
	if err != nil {
		writeAgentExecutionCommandError(writer, err)
		return
	}
	started, err := server.startManagedAttempt(request.Context(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: childCommandMeta(meta, "start-profile-switch"), RunID: runID, ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil {
		if refreshErr := server.publishManagedGenerationReferences(request.Context(), nil); refreshErr != nil {
			err = errors.Join(err, refreshErr)
		}
		if err == nil {
			err = errors.New("profile successor did not create a runtime session")
		}
		writeMutationError(writer, http.StatusInternalServerError, err)
		return
	}
	if started.Session.Identity.Valid() {
		go server.monitorRuntimeRun(agentpi.AdapterID, runID, started.Session.ID, started.Session.Identity)
	}
	view, err := server.runView(request.Context(), runID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) requireAgentExecutionRoute(writer http.ResponseWriter, request *http.Request, binding domain.AgentExecutionProfileBinding) bool {
	if !binding.Bound() {
		writeError(writer, http.StatusServiceUnavailable, errors.New("selected Agent execution route is unavailable"))
		return false
	}
	if _, err := server.registry.Get(agentpi.AdapterID); err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("selected Agent execution route is unavailable"))
		return false
	}
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, binding.ExecutionProvider())
	if err != nil || !server.supervisor.hasTarget(target) {
		writeError(writer, http.StatusServiceUnavailable, errors.New("selected Agent execution route is unavailable"))
		return false
	}
	if binding.Profile() == domain.AgentExecutionProfileIsolatedLocal {
		if err := server.isolatedLocal.available(request.Context()); err != nil {
			writeError(writer, http.StatusServiceUnavailable, err)
			return false
		}
		return true
	}
	if binding.ExecutionProvider() == domain.DockerExecutionProvider {
		if err := server.requireManagedGenerationConfiguration(); err != nil {
			writeError(writer, http.StatusServiceUnavailable, errors.New("selected Agent execution route is unavailable"))
			return false
		}
		if err := server.publishManagedGenerationReferences(request.Context(), nil); err != nil {
			writeError(writer, http.StatusServiceUnavailable, errors.New("selected Agent execution route is unavailable"))
			return false
		}
		return server.requirePiPreflight(writer, request)
	}
	return true
}

func writeAgentExecutionCommandError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrUnauthorizedCommand):
		writeError(writer, http.StatusForbidden, errors.New("Agent execution authorization is required"))
	case errors.Is(err, storecontract.ErrVersionConflict), errors.Is(err, storecontract.ErrIdempotencyConflict), errors.Is(err, storecontract.ErrExecutionProfileConflict):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, storecontract.ErrNotFound):
		writeError(writer, http.StatusNotFound, err)
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, app.ErrInvalidCommand):
		writeError(writer, http.StatusUnprocessableEntity, err)
	default:
		writeError(writer, http.StatusInternalServerError, errors.New("Agent execution command failed"))
	}
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
