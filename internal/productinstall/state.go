package productinstall

import (
	"context"
	"fmt"
)

func (service *Service) loadState(ctx context.Context) (State, error) {
	state, err := service.deps.Store.Load(ctx)
	if err != nil {
		return State{}, err
	}
	if state.SchemaVersion == "" {
		state = EmptyState()
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	return cloneState(state), nil
}

func (service *Service) checkpoint(ctx context.Context, state *State) error {
	expectedRevision := state.Revision
	next := cloneState(*state)
	next.Revision++
	if err := validateState(next); err != nil {
		return err
	}
	if err := service.deps.Store.SaveCAS(ctx, expectedRevision, cloneState(next)); err != nil {
		return err
	}
	*state = next
	return nil
}

// protectedAssetIdentities is the single fail-closed reference barrier used by
// rollback, GC, and uninstall. A dangling reference is treated as corrupt state,
// never as permission to delete an asset.
func (service *Service) protectedAssetIdentities(ctx context.Context, state State) (map[string]bool, error) {
	references, err := service.deps.References.References(ctx)
	if err != nil {
		return nil, err
	}
	protectedGenerations := map[string]bool{}
	if state.ActiveGenerationID != "" {
		protectedGenerations[state.ActiveGenerationID] = true
	}
	for _, generationID := range referencedGenerationValues(references, true) {
		if !identifierPattern.MatchString(generationID) {
			return nil, fmt.Errorf("%w: invalid generation reference", ErrInvalidState)
		}
		protectedGenerations[generationID] = true
	}

	protectedAssets := map[string]bool{}
	for generationID := range protectedGenerations {
		generation, _ := generationByID(&state, generationID)
		if generation == nil || generation.Status == GenerationRemoved {
			return nil, fmt.Errorf("%w: reference to unavailable generation %s", ErrInvalidState, generationID)
		}
		for _, asset := range generation.Assets {
			if !asset.Removed {
				protectedAssets[asset.Spec.Identity] = true
			}
		}
	}
	return protectedAssets, nil
}

func referencedGenerationIDs(ctx context.Context, reader ReferenceReader) (map[string]bool, error) {
	references, err := reader.References(ctx)
	if err != nil {
		return nil, err
	}
	return validatedGenerationIDSet(referencedGenerationValues(references, true))
}

func attemptReferencedGenerationIDs(ctx context.Context, reader ReferenceReader) (map[string]bool, error) {
	references, err := reader.References(ctx)
	if err != nil {
		return nil, err
	}
	return validatedGenerationIDSet(referencedGenerationValues(references, false))
}

func servingGenerationIDs(ctx context.Context, reader ReferenceReader) (map[string]bool, error) {
	references, err := reader.References(ctx)
	if err != nil {
		return nil, err
	}
	return validatedGenerationIDSet(references.ServingGenerationIDs)
}

func referencedGenerationValues(references GenerationReferences, includeServing bool) []string {
	values := make([]string, 0, len(references.ServingGenerationIDs)+len(references.ActiveAttemptGenerationIDs)+len(references.RecoverableAttemptGenerationIDs))
	if includeServing {
		values = append(values, references.ServingGenerationIDs...)
	}
	values = append(values, references.ActiveAttemptGenerationIDs...)
	values = append(values, references.RecoverableAttemptGenerationIDs...)
	return values
}

func validatedGenerationIDSet(values []string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, generationID := range values {
		if !identifierPattern.MatchString(generationID) {
			return nil, fmt.Errorf("%w: invalid generation reference", ErrInvalidState)
		}
		result[generationID] = true
	}
	return result, nil
}

func markAssetRemoved(state *State, identity string) {
	for generationIndex := range state.Generations {
		for assetIndex := range state.Generations[generationIndex].Assets {
			if state.Generations[generationIndex].Assets[assetIndex].Spec.Identity == identity {
				state.Generations[generationIndex].Assets[assetIndex].Removed = true
			}
		}
	}
}
