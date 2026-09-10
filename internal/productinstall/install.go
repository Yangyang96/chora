package productinstall

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

func (service *Service) Setup(ctx context.Context, request InstallRequest) (InstallResult, error) {
	return service.lockedInstall(ctx, ActionSetup, request)
}

func (service *Service) Upgrade(ctx context.Context, request InstallRequest) (InstallResult, error) {
	return service.lockedInstall(ctx, ActionUpgrade, request)
}

func (service *Service) lockedInstall(ctx context.Context, action Action, request InstallRequest) (InstallResult, error) {
	// Authorization precedes every durable or external mutation. In particular,
	// an invalid or absent grant cannot acquire the external lock or create a
	// journal row.
	if err := service.authorizeMutation(ctx, request.Authority, action); err != nil {
		return InstallResult{}, err
	}
	var result InstallResult
	err := service.deps.Locker.WithInstallationLock(ctx, func(lockCtx context.Context) error {
		var operationErr error
		result, operationErr = service.installLocked(lockCtx, action, request)
		return operationErr
	})
	return result, err
}

func (service *Service) installLocked(ctx context.Context, action Action, request InstallRequest) (InstallResult, error) {
	if !identifierPattern.MatchString(request.Key) || validateTarget(request.Target) != nil || validateGeneration(request.Generation) != nil {
		return InstallResult{}, ErrInvalidRequest
	}
	digest, err := requestDigest(struct {
		Action     Action
		Target     EngineTarget
		Generation GenerationSpec
	}{action, request.Target, request.Generation})
	if err != nil {
		return InstallResult{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	state, err := service.loadState(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	operation, operationIndex := operationByKey(&state, request.Key)
	if operation != nil {
		if operation.RequestDigest != digest || operation.Action != action {
			return InstallResult{}, ErrIdempotencyConflict
		}
		switch operation.Phase {
		case PhaseComplete:
			return InstallResult{GenerationID: operation.Generation.GenerationID, Active: state.ActiveGenerationID == operation.Generation.GenerationID, Replayed: true}, nil
		case PhaseFailed:
			return InstallResult{}, fmt.Errorf("%w: %s", ErrOperationFailed, operation.Failure)
		}
	} else {
		if len(state.Operations) >= maxOperations {
			return InstallResult{}, fmt.Errorf("%w: operation journal is full", ErrInvalidState)
		}
		for _, candidate := range state.Operations {
			if candidate.Phase != PhaseComplete && candidate.Phase != PhaseFailed {
				return InstallResult{}, ErrBusy
			}
		}
		if existing, _ := generationByID(&state, request.Generation.GenerationID); existing != nil {
			if !generationSpecsEqual(existing.Spec, request.Generation) {
				return InstallResult{}, ErrAssetConflict
			}
			// Generation identities are immutable. A removed generation cannot be
			// made active again: its assets may already be gone, and activating it
			// before durable completion would split the activation pointer from
			// installation state. A later Setup/Upgrade must use a fresh identity.
			if existing.Status == GenerationRemoved {
				return InstallResult{}, fmt.Errorf("%w: removed generation identity cannot be reused", ErrInvalidRequest)
			}
		}
		if action == ActionSetup && state.ActiveGenerationID != "" && state.ActiveGenerationID != request.Generation.GenerationID {
			return InstallResult{}, fmt.Errorf("%w: setup cannot replace an active generation", ErrInvalidRequest)
		}
		if action == ActionUpgrade && (state.ActiveGenerationID == "" || state.ActiveGenerationID == request.Generation.GenerationID) {
			return InstallResult{}, fmt.Errorf("%w: upgrade requires a distinct active predecessor", ErrInvalidRequest)
		}
		current, currentErr := service.deps.Activator.Current(ctx)
		if currentErr != nil {
			return InstallResult{}, currentErr
		}
		if current != state.ActiveGenerationID {
			return InstallResult{}, ErrConcurrentUpdate
		}
		now := service.deps.Clock.Now()
		state.Operations = append(state.Operations, Operation{
			Key: request.Key, RequestDigest: digest, Action: action, Authority: request.Authority, Phase: PhasePlanned,
			Generation: request.Generation, Target: request.Target,
			PriorActiveGeneration: state.ActiveGenerationID, CreatedAt: now, UpdatedAt: now,
		})
		operationIndex = len(state.Operations) - 1
		if err := service.checkpoint(ctx, &state); err != nil {
			return InstallResult{}, err
		}
	}

	return service.resumeInstall(ctx, &state, operationIndex, operation != nil)
}

func generationSpecsEqual(left, right GenerationSpec) bool {
	return left.SchemaVersion == right.SchemaVersion && left.GenerationID == right.GenerationID &&
		left.ReleaseID == right.ReleaseID && left.ManifestSHA256 == right.ManifestSHA256 &&
		slices.Equal(left.Assets, right.Assets)
}

func (service *Service) resumeInstall(ctx context.Context, state *State, operationIndex int, replayed bool) (InstallResult, error) {
	operation := &state.Operations[operationIndex]
	if operation.Phase == PhaseRollingBack {
		if err := service.resumeInstallRollback(ctx, state, operationIndex); err != nil {
			return InstallResult{}, err
		}
		return InstallResult{}, fmt.Errorf("%w: %s", ErrOperationFailed, state.Operations[operationIndex].Failure)
	}

	if operation.Phase == PhasePlanned || operation.Phase == PhaseAcquiring {
		if err := service.acquireAssets(ctx, state, operationIndex); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return InstallResult{}, err
			}
			return InstallResult{}, service.failInstall(ctx, state, operationIndex, err)
		}
	}

	operation = &state.Operations[operationIndex]
	if operation.Phase == PhaseProbing && operation.QualificationDigest == "" {
		result, err := service.deps.Prober.Probe(ctx, operation.Authority, operation.Target, operation.Generation)
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				return InstallResult{}, err
			}
			return InstallResult{}, service.failInstall(ctx, state, operationIndex, err)
		}
		if !result.Passed || !result.CleanupProven || !digestPattern.MatchString(result.QualificationDigest) || validateObservation(operation.Target, result.Engine) != nil {
			reason := result.Reason
			if reason == "" {
				reason = "Probe did not pass with proven cleanup and stable Engine identity"
			}
			return InstallResult{}, service.failInstall(ctx, state, operationIndex, fmt.Errorf("%w: %s", ErrProbeFailed, reason))
		}
		operation.Probe = result
		operation.QualificationDigest = result.QualificationDigest
		operation.Phase = PhaseVerifying
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return InstallResult{}, err
		}
	}

	operation = &state.Operations[operationIndex]
	if operation.Phase == PhaseVerifying {
		assets := installedAssets(*operation)
		if err := service.deps.Verifier.Verify(ctx, operation.Target, operation.Generation, assets, operation.Probe); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return InstallResult{}, err
			}
			return InstallResult{}, service.failInstall(ctx, state, operationIndex, err)
		}
		operation.Phase = PhaseActivating
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return InstallResult{}, err
		}
	}

	operation = &state.Operations[operationIndex]
	if operation.Phase == PhaseActivating {
		current, err := service.deps.Activator.Current(ctx)
		if err != nil {
			return InstallResult{}, err
		}
		if current != operation.Generation.GenerationID {
			if current != operation.PriorActiveGeneration {
				return InstallResult{}, ErrConcurrentUpdate
			}
			err = service.deps.Activator.AtomicActivate(ctx, operation.PriorActiveGeneration, operation.Generation)
			if err != nil {
				if errors.Is(err, ErrInterrupted) {
					return InstallResult{}, err
				}
				observed, observeErr := service.deps.Activator.Current(ctx)
				if observeErr != nil || observed != operation.Generation.GenerationID {
					if observeErr == nil && observed != operation.PriorActiveGeneration {
						return InstallResult{}, ErrConcurrentUpdate
					}
					return InstallResult{}, service.failInstall(ctx, state, operationIndex, err)
				}
			}
		}
		if err := service.completeActivation(ctx, state, operationIndex); err != nil {
			return InstallResult{}, err
		}
	}

	operation = &state.Operations[operationIndex]
	if operation.Phase != PhaseComplete {
		return InstallResult{}, ErrInvalidState
	}
	return InstallResult{GenerationID: operation.Generation.GenerationID, Active: true, Replayed: replayed}, nil
}

func (service *Service) acquireAssets(ctx context.Context, state *State, operationIndex int) error {
	operation := &state.Operations[operationIndex]
	if operation.Phase == PhasePlanned {
		operation.Phase = PhaseAcquiring
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return err
		}
	}
	for {
		operation = &state.Operations[operationIndex]
		if operation.NextAssetIndex >= len(operation.Generation.Assets) {
			operation.PendingAssetID = ""
			operation.PendingAssetIdentity = ""
			operation.Phase = PhaseProbing
			operation.UpdatedAt = service.deps.Clock.Now()
			return service.checkpoint(ctx, state)
		}
		asset := operation.Generation.Assets[operation.NextAssetIndex]
		status, err := service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
		if err != nil {
			return err
		}
		if status.Present && status.Identity != asset.Identity {
			return fmt.Errorf("%w: %s", ErrAssetConflict, asset.ID)
		}
		if !status.Present && status.Identity != "" {
			return fmt.Errorf("%w: contradictory status for %s", ErrAssetConflict, asset.ID)
		}

		if operation.PendingAssetID == "" && status.Present {
			operation.ReusedAssetIDs = appendUnique(operation.ReusedAssetIDs, asset.ID)
			operation.NextAssetIndex++
			operation.UpdatedAt = service.deps.Clock.Now()
			if err := service.checkpoint(ctx, state); err != nil {
				return err
			}
			continue
		}
		if !status.Present && asset.Ownership != OwnershipChora {
			return fmt.Errorf("%w: required external asset %s is absent", ErrAssetConflict, asset.ID)
		}
		if operation.PendingAssetID == "" {
			operation.PendingAssetID = asset.ID
			operation.PendingAssetIdentity = asset.Identity
			operation.UpdatedAt = service.deps.Clock.Now()
			if err := service.checkpoint(ctx, state); err != nil {
				return err
			}
		}
		if operation.PendingAssetID != asset.ID || operation.PendingAssetIdentity != asset.Identity {
			return ErrInvalidState
		}
		if !status.Present {
			if err := service.deps.Acquirer.AcquirePinned(ctx, operation.Target, asset); err != nil {
				return err
			}
			status, err = service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
			if err != nil {
				return err
			}
			if !status.Present || status.Identity != asset.Identity {
				return fmt.Errorf("%w: acquisition did not produce %s", ErrAssetConflict, asset.ID)
			}
		}
		operation = &state.Operations[operationIndex]
		operation.AcquiredAssetIDs = appendUnique(operation.AcquiredAssetIDs, asset.ID)
		operation.PendingAssetID = ""
		operation.PendingAssetIdentity = ""
		operation.NextAssetIndex++
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return err
		}
	}
}

func (service *Service) completeActivation(ctx context.Context, state *State, operationIndex int) error {
	operation := &state.Operations[operationIndex]
	if existing, _ := generationByID(state, operation.Generation.GenerationID); existing != nil {
		if existing.Status == GenerationActive && state.ActiveGenerationID == operation.Generation.GenerationID {
			operation.Phase = PhaseComplete
			operation.UpdatedAt = service.deps.Clock.Now()
			return service.checkpoint(ctx, state)
		}
		return ErrInvalidState
	}
	if operation.PriorActiveGeneration != "" {
		prior, _ := generationByID(state, operation.PriorActiveGeneration)
		if prior == nil || prior.Status != GenerationActive {
			return ErrInvalidState
		}
		prior.Status = GenerationSuperseded
	}
	state.Generations = append(state.Generations, Generation{
		Spec: operation.Generation, Status: GenerationActive,
		QualificationDigest: operation.QualificationDigest, Target: operation.Target, Probe: operation.Probe,
		Assets:      installedAssets(*operation),
		ActivatedAt: service.deps.Clock.Now(),
	})
	state.ActiveGenerationID = operation.Generation.GenerationID
	operation.Phase = PhaseComplete
	operation.UpdatedAt = service.deps.Clock.Now()
	return service.checkpoint(ctx, state)
}

func (service *Service) failInstall(ctx context.Context, state *State, operationIndex int, cause error) error {
	operation := &state.Operations[operationIndex]
	operation.Failure = cause.Error()
	operation.Phase = PhaseRollingBack
	operation.UpdatedAt = service.deps.Clock.Now()
	if err := service.checkpoint(ctx, state); err != nil {
		return err
	}
	if err := service.resumeInstallRollback(ctx, state, operationIndex); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrOperationFailed, cause)
}

func (service *Service) resumeInstallRollback(ctx context.Context, state *State, operationIndex int) error {
	operation := &state.Operations[operationIndex]
	protected, err := service.protectedAssetIdentities(ctx, *state)
	if err != nil {
		return err
	}
	assetIDs := slices.Clone(operation.AcquiredAssetIDs)
	if operation.PendingAssetID != "" {
		assetIDs = appendUnique(assetIDs, operation.PendingAssetID)
	}
	for _, assetID := range assetIDs {
		asset, ok := assetByID(operation.Generation, assetID)
		if !ok {
			return ErrInvalidState
		}
		if asset.Ownership != OwnershipChora || protected[asset.Identity] {
			continue
		}
		status, inspectErr := service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
		if inspectErr != nil {
			return inspectErr
		}
		if status.Present && status.Identity != asset.Identity {
			return ErrAssetConflict
		}
		if status.Present {
			if removeErr := service.deps.Remover.RemoveExact(ctx, operation.Target, asset); removeErr != nil {
				return removeErr
			}
			status, inspectErr = service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
			if inspectErr != nil || status.Present {
				return fmt.Errorf("rollback removal of %s is unproven", asset.ID)
			}
		}
	}
	operation = &state.Operations[operationIndex]
	operation.PendingAssetID = ""
	operation.PendingAssetIdentity = ""
	operation.Phase = PhaseFailed
	operation.UpdatedAt = service.deps.Clock.Now()
	return service.checkpoint(ctx, state)
}

func installedAssets(operation Operation) []InstalledAsset {
	result := make([]InstalledAsset, 0, len(operation.Generation.Assets))
	for _, asset := range operation.Generation.Assets {
		result = append(result, InstalledAsset{Spec: asset, Reused: contains(operation.ReusedAssetIDs, asset.ID)})
	}
	return result
}

func assetByID(generation GenerationSpec, id string) (AssetSpec, bool) {
	for _, asset := range generation.Assets {
		if asset.ID == id {
			return asset, true
		}
	}
	return AssetSpec{}, false
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
