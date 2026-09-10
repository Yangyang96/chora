package productinstall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/pidistribution"
)

const PrivatePiAssetKind = "private_pi"

type privatePiLifecycle interface {
	Inspect(context.Context) (pidistribution.PrivateBinding, bool, error)
	RemovalPending(context.Context) (bool, error)
	Install(context.Context) (pidistribution.PrivateBinding, error)
	Remove(context.Context) error
}

type resolverPrivatePiLifecycle struct {
	resolver *pidistribution.Resolver
	binding  pidistribution.PrivateBinding
}

func (lifecycle resolverPrivatePiLifecycle) Inspect(ctx context.Context) (pidistribution.PrivateBinding, bool, error) {
	selection, present, err := lifecycle.resolver.InspectPrivate(ctx)
	if err != nil || !present {
		return pidistribution.PrivateBinding{}, present, err
	}
	return pidistribution.PrivateBinding{
		ManifestSHA256: lifecycle.binding.ManifestSHA256,
		ClosureSHA256:  fmt.Sprintf("%x", selection.ClosureSHA256()),
	}, true, nil
}

func (lifecycle resolverPrivatePiLifecycle) Install(ctx context.Context) (pidistribution.PrivateBinding, error) {
	selection, err := lifecycle.resolver.InstallPrivate(ctx)
	if err != nil {
		return pidistribution.PrivateBinding{}, err
	}
	return pidistribution.PrivateBinding{
		ManifestSHA256: lifecycle.binding.ManifestSHA256,
		ClosureSHA256:  fmt.Sprintf("%x", selection.ClosureSHA256()),
	}, nil
}

func (lifecycle resolverPrivatePiLifecycle) RemovalPending(ctx context.Context) (bool, error) {
	return lifecycle.resolver.RemovalPending(ctx)
}

func (lifecycle resolverPrivatePiLifecycle) Remove(ctx context.Context) error {
	return lifecycle.resolver.RemovePrivate(ctx)
}

type ExactPrivatePiAssets struct {
	target    EngineTarget
	binding   pidistribution.PrivateBinding
	lifecycle privatePiLifecycle
}

func NewExactPrivatePiAssets(target EngineTarget, resolver *pidistribution.Resolver, binding pidistribution.PrivateBinding) (*ExactPrivatePiAssets, error) {
	if resolver == nil || validateTarget(target) != nil || !digestPattern.MatchString(binding.ManifestSHA256) || !digestPattern.MatchString(binding.ClosureSHA256) {
		return nil, ErrInvalidRequest
	}
	return &ExactPrivatePiAssets{
		target: target, binding: binding,
		lifecycle: resolverPrivatePiLifecycle{resolver: resolver, binding: binding},
	}, nil
}

func privatePiAssetSpec(binding pidistribution.PrivateBinding) (AssetSpec, error) {
	asset := AssetSpec{
		ID: "private_pi", Kind: PrivatePiAssetKind, Identity: binding.ClosureSHA256,
		Ownership: OwnershipChora, AcquisitionKind: AcquisitionPrivateRuntime,
		AcquisitionRef: "manifest-sha256:" + binding.ManifestSHA256,
	}
	if validateAsset(asset) != nil || !digestPattern.MatchString(binding.ManifestSHA256) {
		return AssetSpec{}, ErrInvalidRequest
	}
	return asset, nil
}

func (adapter *ExactPrivatePiAssets) InspectExact(ctx context.Context, target EngineTarget, asset AssetSpec) (AssetStatus, error) {
	if err := adapter.require(target, asset); err != nil {
		return AssetStatus{}, err
	}
	binding, present, err := adapter.lifecycle.Inspect(ctx)
	if err != nil {
		return AssetStatus{}, err
	}
	if !present {
		pending, pendingErr := adapter.lifecycle.RemovalPending(ctx)
		if pendingErr != nil {
			return AssetStatus{}, pendingErr
		}
		if pending {
			return AssetStatus{Present: true, Identity: adapter.binding.ClosureSHA256}, nil
		}
		return AssetStatus{}, nil
	}
	if binding != adapter.binding {
		return AssetStatus{Present: true, Identity: binding.ClosureSHA256}, nil
	}
	return AssetStatus{Present: true, Identity: binding.ClosureSHA256}, nil
}

func (adapter *ExactPrivatePiAssets) AcquirePinned(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	if err := adapter.require(target, asset); err != nil {
		return err
	}
	binding, err := adapter.lifecycle.Install(ctx)
	if err != nil {
		if errors.Is(err, ErrInterrupted) || errors.Is(err, pidistribution.ErrRemovalInterrupted) {
			return ErrInterrupted
		}
		return errors.New("install exact authenticated private Pi failed")
	}
	if binding != adapter.binding {
		return ErrAssetConflict
	}
	return nil
}

func (adapter *ExactPrivatePiAssets) RemoveExact(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	if err := adapter.require(target, asset); err != nil {
		return err
	}
	if err := adapter.lifecycle.Remove(ctx); err != nil {
		if errors.Is(err, ErrInterrupted) || errors.Is(err, pidistribution.ErrRemovalInterrupted) {
			return ErrInterrupted
		}
		return errors.New("remove exact authenticated private Pi failed")
	}
	return nil
}

func (adapter *ExactPrivatePiAssets) require(target EngineTarget, asset AssetSpec) error {
	if adapter == nil || adapter.lifecycle == nil {
		return ErrInvalidRequest
	}
	expected, err := privatePiAssetSpec(adapter.binding)
	if err != nil || target != adapter.target || asset != expected {
		return ErrInvalidRequest
	}
	return nil
}

// ExactProductAssets dispatches only the two manifest-bound asset families.
// Unknown kinds and PATH Pi never reach a mutating adapter.
type ExactProductAssets struct {
	docker    *ExactDockerAssets
	privatePi *ExactPrivatePiAssets
}

func NewExactProductAssets(docker *ExactDockerAssets, privatePi *ExactPrivatePiAssets) (*ExactProductAssets, error) {
	if docker == nil || privatePi == nil {
		return nil, ErrInvalidRequest
	}
	return &ExactProductAssets{docker: docker, privatePi: privatePi}, nil
}

func (adapter *ExactProductAssets) InspectExact(ctx context.Context, target EngineTarget, asset AssetSpec) (AssetStatus, error) {
	switch asset.Kind {
	case dockerImageKind:
		return adapter.docker.InspectExact(ctx, target, asset)
	case PrivatePiAssetKind:
		return adapter.privatePi.InspectExact(ctx, target, asset)
	default:
		return AssetStatus{}, ErrInvalidRequest
	}
}

func (adapter *ExactProductAssets) AcquirePinned(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	switch asset.Kind {
	case dockerImageKind:
		return adapter.docker.AcquirePinned(ctx, target, asset)
	case PrivatePiAssetKind:
		return adapter.privatePi.AcquirePinned(ctx, target, asset)
	default:
		return ErrInvalidRequest
	}
}

func (adapter *ExactProductAssets) RemoveExact(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	switch asset.Kind {
	case dockerImageKind:
		return adapter.docker.RemoveExact(ctx, target, asset)
	case PrivatePiAssetKind:
		return adapter.privatePi.RemoveExact(ctx, target, asset)
	default:
		return ErrInvalidRequest
	}
}

func (adapter *ExactProductAssets) Verify(ctx context.Context, target EngineTarget, generation GenerationSpec, assets []InstalledAsset, probe ProbeResult) error {
	if validateGeneration(generation) != nil || len(assets) != len(generation.Assets) || !probe.Passed || !probe.CleanupProven || validateObservation(target, probe.Engine) != nil {
		return ErrInvalidRequest
	}
	for index, asset := range generation.Assets {
		if assets[index].Spec != asset {
			return ErrAssetConflict
		}
		status, err := adapter.InspectExact(ctx, target, asset)
		if err != nil || !status.Present || status.Identity != asset.Identity {
			return ErrAssetConflict
		}
	}
	return nil
}

func BindPrivatePi(bound BoundRelease, binding pidistribution.PrivateBinding) (BoundRelease, error) {
	asset, err := privatePiAssetSpec(binding)
	if err != nil {
		return BoundRelease{}, err
	}
	bound.Generation.Assets = append(bound.Generation.Assets, asset)
	slices.SortFunc(bound.Generation.Assets, func(left, right AssetSpec) int { return strings.Compare(left.ID, right.ID) })
	if validateGeneration(bound.Generation) != nil {
		return BoundRelease{}, ErrInvalidRequest
	}
	return bound, nil
}
