package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/productinstall"
)

type productInstallationController struct {
	mu                 sync.Mutex
	backend            *productinstall.FileBackend
	executor           productExecutor
	candidate          productCommand
	loadedGenerationID string
}

func newProductInstallationController(candidate productCommand, executor productExecutor) (*productInstallationController, error) {
	if executor == nil || candidate.action != productinstall.ActionSetup || !canonicalAbsolute(candidate.stateRoot) ||
		!productIdentifier.MatchString(candidate.generationID) || candidate.target.CLIPath == "" ||
		candidate.target.CLIPath != filepath.Join(candidate.colima.ToolRoot, "docker-"+dockerClientVersion) {
		return nil, productinstall.ErrInvalidRequest
	}
	backend, err := productinstall.NewFileBackend(candidate.stateRoot)
	if err != nil {
		return nil, err
	}
	state, err := backend.Load(context.Background())
	if err != nil {
		return nil, err
	}
	return &productInstallationController{
		backend: backend, executor: executor, candidate: candidate, loadedGenerationID: state.ActiveGenerationID,
	}, nil
}

func (controller *productInstallationController) Doctor(ctx context.Context) (localweb.ProductInstallationView, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.doctorLocked(ctx)
}

func (controller *productInstallationController) doctorLocked(ctx context.Context) (localweb.ProductInstallationView, error) {
	if controller == nil || controller.backend == nil || controller.executor == nil {
		return localweb.ProductInstallationView{}, productinstall.ErrInvalidState
	}
	view := localweb.ProductInstallationView{
		SchemaVersion: localweb.ProductInstallationSchemaVersion, Status: localweb.ProductInstallationStatusReady,
		CandidateGenerationID: controller.candidate.generationID,
	}
	state, err := controller.backend.Load(ctx)
	if err != nil {
		return localweb.ProductInstallationView{}, err
	}
	view.ActiveGenerationID = state.ActiveGenerationID
	view.RestartRequired = controller.loadedGenerationID != state.ActiveGenerationID
	if !view.RestartRequired {
		view.Actions = localweb.ProductInstallationActionsView{
			Setup: state.ActiveGenerationID == "", Upgrade: state.ActiveGenerationID != "" && state.ActiveGenerationID != controller.candidate.generationID,
			GC: state.ActiveGenerationID != "", Uninstall: state.ActiveGenerationID != "",
		}
	}
	doctorCommand := controller.candidate
	doctorCommand.action = "doctor"
	doctorCommand.authority = productinstall.MutationAuthority{}
	doctorCommand.key = ""
	result, doctorErr := controller.executor.Execute(ctx, doctorCommand)
	if doctorErr != nil || result.Status != "passed" {
		view.Status = localweb.ProductInstallationStatusBlocked
		if result.Reason == productinstall.DoctorReasonIdentityMismatch {
			view.ReasonCode = localweb.ProductInstallationReasonIdentityMismatch
		} else {
			view.ReasonCode = localweb.ProductInstallationReasonObservationUnavailable
		}
		return view, nil
	}
	view.Engine = localweb.ProductInstallationEngineView{
		Ready: true, APIVersion: result.APIVersion, OperatingSystem: result.OperatingSystem,
		Architecture: result.Architecture, ContextName: result.ContextName,
	}
	return view, nil
}

func (controller *productInstallationController) Mutate(ctx context.Context, request localweb.ProductInstallationMutationRequest) (localweb.ProductInstallationView, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller == nil || controller.backend == nil || controller.executor == nil || request.ActorID == "" || request.SessionID == "" || request.IdempotencyKey == "" {
		return localweb.ProductInstallationView{}, productinstall.ErrAuthorityRequired
	}
	command := controller.candidate
	action, err := controllerAction(request.Action)
	if err != nil {
		return localweb.ProductInstallationView{}, err
	}
	command.action = action
	command.key = derivedControllerToken("key", request.ActorID, request.SessionID, request.IdempotencyKey, string(action))
	command.authority = productinstall.MutationAuthority{
		GrantID: derivedControllerToken("grant", request.ActorID, request.SessionID, request.IdempotencyKey, string(action)),
		ActorID: derivedControllerToken("actor", request.ActorID), SessionID: derivedControllerToken("session", request.SessionID),
		Action: action, IssuedAt: time.Now().UTC(),
	}
	command.limit = request.Limit
	state, err := controller.backend.Load(ctx)
	if err != nil {
		return localweb.ProductInstallationView{}, err
	}
	if controller.loadedGenerationID != state.ActiveGenerationID && !knownControllerReplay(state, command) {
		return localweb.ProductInstallationView{}, productinstall.ErrInvalidState
	}
	if action == productinstall.ActionSetup {
		if !request.ConfirmPinnedEngineTools || !request.ConfirmColimaVM {
			return localweb.ProductInstallationView{}, productinstall.ErrAuthorityRequired
		}
		command.colima.Enabled = true
		command.colima.DownloadAuthorization = productionColimaPolicy().downloadGrant()
		command.colima.VMAuthorization = command.colima.expectedVMAuthorization()
	}
	result, err := controller.executor.Execute(ctx, command)
	if err != nil {
		return localweb.ProductInstallationView{}, err
	}
	view, err := controller.doctorLocked(ctx)
	if err != nil {
		return localweb.ProductInstallationView{}, err
	}
	view.Replayed = result.Replayed
	if view.Status == localweb.ProductInstallationStatusBlocked {
		return view, nil
	}
	view.Status = localweb.ProductInstallationStatusComplete
	view.ReasonCode = ""
	return view, nil
}

func knownControllerReplay(state productinstall.State, command productCommand) bool {
	for _, operation := range state.Operations {
		if operation.Key == command.key && operation.Action == command.action {
			return true
		}
	}
	return false
}

func controllerAction(action localweb.ProductInstallationAction) (productinstall.Action, error) {
	switch action {
	case localweb.ProductInstallationActionSetup:
		return productinstall.ActionSetup, nil
	case localweb.ProductInstallationActionUpgrade:
		return productinstall.ActionUpgrade, nil
	case localweb.ProductInstallationActionGC:
		return productinstall.ActionGarbageGC, nil
	case localweb.ProductInstallationActionUninstall:
		return productinstall.ActionUninstall, nil
	default:
		return "", productinstall.ErrInvalidRequest
	}
}

func derivedControllerToken(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("chora-product-controller-v1\x00" + kind))
	for _, value := range values {
		_, _ = hash.Write([]byte("\x00" + value))
	}
	return fmt.Sprintf("%s-%x", kind, hash.Sum(nil)[:16])
}

var _ localweb.ProductInstallationController = (*productInstallationController)(nil)
