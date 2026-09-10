package productinstall

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

const maxGCAssets = 64

type removalItem struct {
	generationID string
	asset        AssetSpec
}

func (service *Service) GarbageCollect(ctx context.Context, request GCRequest) (GCResult, error) {
	if err := service.authorizeMutation(ctx, request.Authority, ActionGarbageGC); err != nil {
		return GCResult{}, err
	}
	var result GCResult
	err := service.deps.Locker.WithInstallationLock(ctx, func(lockCtx context.Context) error {
		var operationErr error
		result, operationErr = service.garbageCollectLocked(lockCtx, request)
		return operationErr
	})
	return result, err
}

func (service *Service) garbageCollectLocked(ctx context.Context, request GCRequest) (GCResult, error) {
	if !identifierPattern.MatchString(request.Key) || validateTarget(request.Target) != nil || request.Limit < 1 || request.Limit > maxGCAssets {
		return GCResult{}, ErrInvalidRequest
	}
	if err := service.deps.References.RequireReferenceSnapshot(ctx); err != nil {
		return GCResult{}, err
	}
	digest, err := requestDigest(struct {
		Action Action
		Target EngineTarget
		Limit  int
	}{ActionGarbageGC, request.Target, request.Limit})
	if err != nil {
		return GCResult{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.loadState(ctx)
	if err != nil {
		return GCResult{}, err
	}
	operation, operationIndex := operationByKey(&state, request.Key)
	if operation != nil {
		if operation.Action != ActionGarbageGC || operation.RequestDigest != digest {
			return GCResult{}, ErrIdempotencyConflict
		}
		if operation.Phase == PhaseComplete {
			return gcResult(*operation, true), nil
		}
		if operation.Phase == PhaseFailed {
			return GCResult{}, fmt.Errorf("%w: %s", ErrOperationFailed, operation.Failure)
		}
		return service.resumeGC(ctx, &state, operationIndex, request.Limit, true)
	}
	if err := ensureOperationSlot(state); err != nil {
		return GCResult{}, err
	}
	current, err := service.deps.Activator.Current(ctx)
	if err != nil {
		return GCResult{}, err
	}
	if current != state.ActiveGenerationID {
		return GCResult{}, ErrConcurrentUpdate
	}
	referenced, err := referencedGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return GCResult{}, err
	}
	targets := []string{}
	skipped := []string{}
	for _, generation := range state.Generations {
		if generation.Status != GenerationSuperseded {
			continue
		}
		if referenced[generation.Spec.GenerationID] {
			skipped = append(skipped, generation.Spec.GenerationID)
			continue
		}
		targets = append(targets, generation.Spec.GenerationID)
	}
	slices.Sort(targets)
	slices.Sort(skipped)
	now := service.deps.Clock.Now()
	state.Operations = append(state.Operations, Operation{
		Key: request.Key, RequestDigest: digest, Action: ActionGarbageGC,
		Authority: request.Authority, Phase: PhaseRemoving, Target: request.Target,
		TargetGenerationIDs: targets, SkippedGenerationIDs: skipped,
		CreatedAt: now, UpdatedAt: now,
	})
	operationIndex = len(state.Operations) - 1
	if err := service.checkpoint(ctx, &state); err != nil {
		return GCResult{}, err
	}
	return service.resumeGC(ctx, &state, operationIndex, request.Limit, false)
}

func (service *Service) resumeGC(ctx context.Context, state *State, operationIndex, limit int, replayed bool) (GCResult, error) {
	items, err := removalItems(*state, state.Operations[operationIndex].TargetGenerationIDs)
	if err != nil {
		return GCResult{}, err
	}
	for _, item := range items {
		operation := &state.Operations[operationIndex]
		if len(operation.RemovedAssetIdentities) >= limit {
			break
		}
		generation, _ := generationByID(state, item.generationID)
		if generation == nil || generation.Status == GenerationRemoved || installedAssetRemoved(*generation, item.asset.Identity) {
			continue
		}
		referenced, referenceErr := referencedGenerationIDs(ctx, service.deps.References)
		if referenceErr != nil {
			return GCResult{}, referenceErr
		}
		if referenced[item.generationID] {
			operation.SkippedGenerationIDs = appendUnique(operation.SkippedGenerationIDs, item.generationID)
			operation.UpdatedAt = service.deps.Clock.Now()
			if err := service.checkpoint(ctx, state); err != nil {
				return GCResult{}, err
			}
			continue
		}
		protected, protectErr := service.protectedAssetIdentities(ctx, *state)
		if protectErr != nil {
			return GCResult{}, protectErr
		}
		if protected[item.asset.Identity] {
			continue
		}
		if err := service.removeAsset(ctx, state, operationIndex, item.asset); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return GCResult{}, err
			}
			return GCResult{}, service.failRemovalOperation(ctx, state, operationIndex, err)
		}
	}

	protected, err := service.protectedAssetIdentities(ctx, *state)
	if err != nil {
		return GCResult{}, err
	}
	referenced, err := referencedGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return GCResult{}, err
	}
	operation := &state.Operations[operationIndex]
	for _, generationID := range operation.TargetGenerationIDs {
		generation, _ := generationByID(state, generationID)
		if generation == nil || referenced[generationID] {
			continue
		}
		if generationCollectionComplete(*generation, protected, operation.RemovedAssetIdentities) {
			generation.Status = GenerationRemoved
		}
	}
	operation = &state.Operations[operationIndex]
	operation.PendingAssetID = ""
	operation.PendingAssetIdentity = ""
	operation.Phase = PhaseComplete
	operation.UpdatedAt = service.deps.Clock.Now()
	if err := service.checkpoint(ctx, state); err != nil {
		return GCResult{}, err
	}
	return gcResult(state.Operations[operationIndex], replayed), nil
}

func (service *Service) Uninstall(ctx context.Context, request UninstallRequest) (UninstallResult, error) {
	if err := service.authorizeMutation(ctx, request.Authority, ActionUninstall); err != nil {
		return UninstallResult{}, err
	}
	var result UninstallResult
	err := service.deps.Locker.WithInstallationLock(ctx, func(lockCtx context.Context) error {
		var operationErr error
		result, operationErr = service.uninstallLocked(lockCtx, request)
		return operationErr
	})
	return result, err
}

func (service *Service) uninstallLocked(ctx context.Context, request UninstallRequest) (UninstallResult, error) {
	if !identifierPattern.MatchString(request.Key) || validateTarget(request.Target) != nil {
		return UninstallResult{}, ErrInvalidRequest
	}
	if err := service.deps.References.RequireReferenceSnapshot(ctx); err != nil {
		return UninstallResult{}, err
	}
	targetIDs := slices.Clone(request.GenerationIDs)
	slices.Sort(targetIDs)
	targetIDs = slices.Compact(targetIDs)
	for _, generationID := range targetIDs {
		if !identifierPattern.MatchString(generationID) {
			return UninstallResult{}, ErrInvalidRequest
		}
	}
	digest, err := requestDigest(struct {
		Action        Action
		Target        EngineTarget
		GenerationIDs []string
	}{ActionUninstall, request.Target, targetIDs})
	if err != nil {
		return UninstallResult{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.loadState(ctx)
	if err != nil {
		return UninstallResult{}, err
	}
	operation, operationIndex := operationByKey(&state, request.Key)
	if operation != nil {
		if operation.Action != ActionUninstall || operation.RequestDigest != digest {
			return UninstallResult{}, ErrIdempotencyConflict
		}
		if operation.Phase == PhaseComplete {
			return uninstallResult(*operation, true), nil
		}
		if operation.Phase == PhaseFailed {
			return UninstallResult{}, fmt.Errorf("%w: %s", ErrOperationFailed, operation.Failure)
		}
		return service.resumeUninstall(ctx, &state, operationIndex, true)
	}
	if err := ensureOperationSlot(state); err != nil {
		return UninstallResult{}, err
	}
	if len(targetIDs) == 0 {
		for _, generation := range state.Generations {
			if generation.Status != GenerationRemoved {
				targetIDs = append(targetIDs, generation.Spec.GenerationID)
			}
		}
		slices.Sort(targetIDs)
		// The canonical empty selection means "all installed generations";
		// selected IDs are journaled but deliberately do not change its digest.
	}
	for _, generationID := range targetIDs {
		generation, _ := generationByID(&state, generationID)
		if generation == nil || generation.Status == GenerationRemoved {
			return UninstallResult{}, ErrInvalidRequest
		}
	}
	attemptReferenced, err := attemptReferencedGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return UninstallResult{}, err
	}
	serving, err := servingGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return UninstallResult{}, err
	}
	for _, generationID := range targetIDs {
		if attemptReferenced[generationID] || serving[generationID] && generationID != state.ActiveGenerationID {
			return UninstallResult{}, fmt.Errorf("%w: %s", ErrGenerationReferenced, generationID)
		}
	}
	current, err := service.deps.Activator.Current(ctx)
	if err != nil {
		return UninstallResult{}, err
	}
	if current != state.ActiveGenerationID {
		return UninstallResult{}, ErrConcurrentUpdate
	}
	phase := PhaseRemoving
	if slices.Contains(targetIDs, state.ActiveGenerationID) && state.ActiveGenerationID != "" {
		phase = PhaseDeactivating
	}
	now := service.deps.Clock.Now()
	state.Operations = append(state.Operations, Operation{
		Key: request.Key, RequestDigest: digest, Action: ActionUninstall,
		Authority: request.Authority, Phase: phase, Target: request.Target,
		PriorActiveGeneration: state.ActiveGenerationID, TargetGenerationIDs: targetIDs,
		CreatedAt: now, UpdatedAt: now,
	})
	operationIndex = len(state.Operations) - 1
	if err := service.checkpoint(ctx, &state); err != nil {
		return UninstallResult{}, err
	}
	return service.resumeUninstall(ctx, &state, operationIndex, false)
}

func (service *Service) resumeUninstall(ctx context.Context, state *State, operationIndex int, replayed bool) (UninstallResult, error) {
	operation := &state.Operations[operationIndex]
	retiringServingGeneration := ""
	if operation.PriorActiveGeneration != "" && slices.Contains(operation.TargetGenerationIDs, operation.PriorActiveGeneration) {
		retiringServingGeneration = operation.PriorActiveGeneration
	}
	if err := service.ensureUninstallReferences(ctx, operation.TargetGenerationIDs, retiringServingGeneration); err != nil {
		return UninstallResult{}, service.failRemovalOperation(ctx, state, operationIndex, err)
	}
	if operation.Phase == PhaseDeactivating {
		current, currentErr := service.deps.Activator.Current(ctx)
		if currentErr != nil {
			return UninstallResult{}, currentErr
		}
		if current == operation.PriorActiveGeneration {
			deactivateErr := service.deps.Activator.AtomicDeactivate(ctx, operation.PriorActiveGeneration)
			if errors.Is(deactivateErr, ErrInterrupted) {
				return UninstallResult{}, deactivateErr
			}
			if deactivateErr != nil {
				observed, observeErr := service.deps.Activator.Current(ctx)
				if observeErr != nil || observed != "" {
					return UninstallResult{}, service.failRemovalOperation(ctx, state, operationIndex, deactivateErr)
				}
			}
		} else if current != "" {
			return UninstallResult{}, ErrConcurrentUpdate
		}
		if state.ActiveGenerationID == operation.PriorActiveGeneration {
			generation, _ := generationByID(state, operation.PriorActiveGeneration)
			if generation == nil || generation.Status != GenerationActive {
				return UninstallResult{}, ErrInvalidState
			}
			generation.Status = GenerationSuperseded
			state.ActiveGenerationID = ""
		}
		operation = &state.Operations[operationIndex]
		operation.Phase = PhaseRemoving
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return UninstallResult{}, err
		}
	}
	if retiringServingGeneration != "" {
		if state.ActiveGenerationID == retiringServingGeneration {
			return UninstallResult{}, ErrInvalidState
		}
		if err := service.deps.References.RetireServingGeneration(ctx, retiringServingGeneration); err != nil {
			return UninstallResult{}, err
		}
		operation = &state.Operations[operationIndex]
		if operation.RetiredServingGenerationID == "" {
			operation.RetiredServingGenerationID = retiringServingGeneration
			operation.UpdatedAt = service.deps.Clock.Now()
			if err := service.checkpoint(ctx, state); err != nil {
				return UninstallResult{}, err
			}
		} else if operation.RetiredServingGenerationID != retiringServingGeneration {
			return UninstallResult{}, ErrInvalidState
		}
	}
	operation = &state.Operations[operationIndex]
	if err := service.ensureUninstallReferences(ctx, operation.TargetGenerationIDs, ""); err != nil {
		return UninstallResult{}, service.failRemovalOperation(ctx, state, operationIndex, err)
	}

	items, err := removalItems(*state, state.Operations[operationIndex].TargetGenerationIDs)
	if err != nil {
		return UninstallResult{}, err
	}
	for _, item := range items {
		operation = &state.Operations[operationIndex]
		if err := service.ensureUninstallReferences(ctx, []string{item.generationID}, ""); err != nil {
			return UninstallResult{}, service.failRemovalOperation(ctx, state, operationIndex,
				err)
		}
		if item.asset.Ownership != OwnershipChora || installedAssetRemovedByID(*state, item.generationID, item.asset.Identity) {
			continue
		}
		if assetNeededOutsideTargets(*state, operation.TargetGenerationIDs, item.asset.Identity) {
			continue
		}
		if err := service.removeAsset(ctx, state, operationIndex, item.asset); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return UninstallResult{}, err
			}
			return UninstallResult{}, service.failRemovalOperation(ctx, state, operationIndex, err)
		}
	}
	operation = &state.Operations[operationIndex]
	for _, generationID := range operation.TargetGenerationIDs {
		generation, _ := generationByID(state, generationID)
		if generation == nil {
			return UninstallResult{}, ErrInvalidState
		}
		generation.Status = GenerationRemoved
	}
	operation = &state.Operations[operationIndex]
	operation.PendingAssetID = ""
	operation.PendingAssetIdentity = ""
	operation.Phase = PhaseComplete
	operation.UpdatedAt = service.deps.Clock.Now()
	if err := service.checkpoint(ctx, state); err != nil {
		return UninstallResult{}, err
	}
	return uninstallResult(state.Operations[operationIndex], replayed), nil
}

func (service *Service) ensureUninstallReferences(ctx context.Context, targetGenerationIDs []string, permittedServingGeneration string) error {
	attemptReferenced, err := attemptReferencedGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return err
	}
	serving, err := servingGenerationIDs(ctx, service.deps.References)
	if err != nil {
		return err
	}
	for _, generationID := range targetGenerationIDs {
		if attemptReferenced[generationID] || serving[generationID] && generationID != permittedServingGeneration {
			return fmt.Errorf("%w: %s", ErrGenerationReferenced, generationID)
		}
	}
	return nil
}

func (service *Service) removeAsset(ctx context.Context, state *State, operationIndex int, asset AssetSpec) error {
	operation := &state.Operations[operationIndex]
	if operation.PendingAssetID != "" && (operation.PendingAssetID != asset.ID || operation.PendingAssetIdentity != asset.Identity) {
		return ErrInvalidState
	}
	status, err := service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
	if err != nil {
		return err
	}
	if status.Present && status.Identity != asset.Identity || !status.Present && status.Identity != "" {
		return ErrAssetConflict
	}
	if status.Present && operation.PendingAssetID == "" {
		operation.PendingAssetID = asset.ID
		operation.PendingAssetIdentity = asset.Identity
		operation.UpdatedAt = service.deps.Clock.Now()
		if err := service.checkpoint(ctx, state); err != nil {
			return err
		}
	}
	if status.Present {
		removeErr := service.deps.Remover.RemoveExact(ctx, operation.Target, asset)
		if errors.Is(removeErr, ErrInterrupted) {
			return removeErr
		}
		status, err = service.deps.Inspector.InspectExact(ctx, operation.Target, asset)
		if err != nil {
			return err
		}
		if removeErr != nil && status.Present {
			return removeErr
		}
		if status.Present || status.Identity != "" {
			return fmt.Errorf("removal of exact asset %s is unproven", asset.ID)
		}
	}
	operation = &state.Operations[operationIndex]
	if !slices.Contains(operation.RemovedAssetIdentities, asset.Identity) {
		operation.RemovedAssetIdentities = append(operation.RemovedAssetIdentities, asset.Identity)
		operation.RemovedAssetIDs = append(operation.RemovedAssetIDs, asset.ID)
	}
	markAssetRemoved(state, asset.Identity)
	operation = &state.Operations[operationIndex]
	operation.PendingAssetID = ""
	operation.PendingAssetIdentity = ""
	operation.UpdatedAt = service.deps.Clock.Now()
	return service.checkpoint(ctx, state)
}

func (service *Service) failRemovalOperation(ctx context.Context, state *State, operationIndex int, cause error) error {
	operation := &state.Operations[operationIndex]
	operation.Failure = cause.Error()
	operation.Phase = PhaseFailed
	operation.UpdatedAt = service.deps.Clock.Now()
	if err := service.checkpoint(ctx, state); err != nil {
		return err
	}
	return errors.Join(ErrOperationFailed, cause)
}

func ensureOperationSlot(state State) error {
	if len(state.Operations) >= maxOperations {
		return fmt.Errorf("%w: operation journal is full", ErrInvalidState)
	}
	for _, operation := range state.Operations {
		if operation.Phase != PhaseComplete && operation.Phase != PhaseFailed {
			return ErrBusy
		}
	}
	return nil
}

func removalItems(state State, generationIDs []string) ([]removalItem, error) {
	result := []removalItem{}
	for _, generationID := range generationIDs {
		generation, _ := generationByID(&state, generationID)
		if generation == nil {
			return nil, ErrInvalidState
		}
		for _, asset := range generation.Spec.Assets {
			if asset.Ownership == OwnershipChora {
				result = append(result, removalItem{generationID: generationID, asset: asset})
			}
		}
	}
	return result, nil
}

func installedAssetRemoved(generation Generation, identity string) bool {
	for _, asset := range generation.Assets {
		if asset.Spec.Identity == identity {
			return asset.Removed
		}
	}
	return false
}

func installedAssetRemovedByID(state State, generationID, identity string) bool {
	generation, _ := generationByID(&state, generationID)
	return generation != nil && installedAssetRemoved(*generation, identity)
}

func assetNeededOutsideTargets(state State, targetGenerationIDs []string, identity string) bool {
	for _, generation := range state.Generations {
		if generation.Status == GenerationRemoved || slices.Contains(targetGenerationIDs, generation.Spec.GenerationID) {
			continue
		}
		for _, asset := range generation.Assets {
			if !asset.Removed && asset.Spec.Identity == identity {
				return true
			}
		}
	}
	return false
}

func generationCollectionComplete(generation Generation, protected map[string]bool, removed []string) bool {
	for _, asset := range generation.Assets {
		if asset.Spec.Ownership == OwnershipExternal || asset.Removed || protected[asset.Spec.Identity] || slices.Contains(removed, asset.Spec.Identity) {
			continue
		}
		return false
	}
	return true
}

func gcResult(operation Operation, replayed bool) GCResult {
	return GCResult{
		RemovedAssetIDs:      slices.Clone(operation.RemovedAssetIDs),
		SkippedGenerationIDs: slices.Clone(operation.SkippedGenerationIDs),
		Replayed:             replayed,
	}
}

func uninstallResult(operation Operation, replayed bool) UninstallResult {
	return UninstallResult{RemovedAssetIDs: slices.Clone(operation.RemovedAssetIDs), Replayed: replayed}
}
