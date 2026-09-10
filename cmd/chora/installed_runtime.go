package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

type installedRuntimeInput struct {
	StateRoot             string
	GenerationID          string
	Target                productinstall.EngineTarget
	Release               releaseassets.ActivationInput
	PrivatePi             productCommand
	AllowPrivatePiInstall bool
}

func loadInstalledRuntime(ctx context.Context, input installedRuntimeInput) (result localweb.ProductOptions, resultErr error) {
	backend, err := productinstall.NewFileBackend(input.StateRoot)
	if err != nil {
		return localweb.ProductOptions{}, productinstall.ErrInvalidRequest
	}
	state, err := backend.Load(ctx)
	if err != nil || state.ActiveGenerationID != input.GenerationID {
		return localweb.ProductOptions{}, errors.New("installed active generation is unavailable")
	}
	current, err := backend.Current(ctx)
	if err != nil || current != input.GenerationID {
		return localweb.ProductOptions{}, errors.New("installed activation identity is unavailable")
	}
	var active *productinstall.Generation
	for index := range state.Generations {
		if state.Generations[index].Spec.GenerationID == input.GenerationID {
			active = &state.Generations[index]
			break
		}
	}
	if active == nil || active.Status != productinstall.GenerationActive || active.Target != input.Target || active.QualificationDigest == "" {
		return localweb.ProductOptions{}, errors.New("installed generation target is unavailable")
	}
	var contract dockersupervisor.CapabilityProbeContract
	var qualification dockersupervisor.EngineQualification
	if !active.Probe.Passed || !active.Probe.CleanupProven || active.Probe.QualificationDigest != active.QualificationDigest ||
		len(active.Probe.CapabilityContract) == 0 || len(active.Probe.EngineQualification) == 0 ||
		json.Unmarshal(active.Probe.CapabilityContract, &contract) != nil || json.Unmarshal(active.Probe.EngineQualification, &qualification) != nil ||
		qualification.Digest() != active.QualificationDigest {
		return localweb.ProductOptions{}, errors.New("installed Engine qualification is unavailable")
	}
	release, err := releaseassets.LoadActivatedRelease(input.Release)
	if err != nil {
		return localweb.ProductOptions{}, errors.New("installed release evidence is unavailable")
	}
	bound, err := productinstall.BindActivatedRelease(release, input.GenerationID, input.Release.ManifestSHA256, agentpi.PolicySHA256)
	if err != nil {
		return localweb.ProductOptions{}, err
	}
	resolver, binding, err := loadPrivatePiLifecycle(input.PrivatePi)
	if err != nil {
		return localweb.ProductOptions{}, err
	}
	hasPrivatePi := false
	for _, asset := range active.Spec.Assets {
		if asset.Kind == productinstall.PrivatePiAssetKind {
			hasPrivatePi = true
			break
		}
	}
	if hasPrivatePi {
		bound, err = productinstall.BindPrivatePi(bound, binding)
		if err != nil {
			return localweb.ProductOptions{}, err
		}
	}
	if !reflect.DeepEqual(active.Spec, bound.Generation) {
		return localweb.ProductOptions{}, errors.New("installed release does not match active generation")
	}
	underlyingRunner, _, err := exactDockerRunner(input.Target)
	if err != nil {
		return localweb.ProductOptions{}, err
	}
	operationLedger, err := dockersupervisor.OpenOperationLedger(underlyingRunner, input.StateRoot, input.GenerationID)
	if err != nil {
		return localweb.ProductOptions{}, errors.New("installed Docker operation ledger is unavailable")
	}
	keepLedger := false
	defer func() {
		if !keepLedger {
			resultErr = errors.Join(resultErr, operationLedger.Close())
		}
	}()
	startupRunner := dockersupervisor.RunnerForOperationPhase(operationLedger, dockersupervisor.OperationPhaseRestart)
	identity, err := dockersupervisor.ObserveEngine(ctx, startupRunner)
	if err != nil || identity.ContextName() != input.Target.ContextName || identity.ContextEndpointDigest() != input.Target.EndpointDigest {
		return localweb.ProductOptions{}, errors.New("installed Engine identity is unavailable")
	}
	expectedContract, err := dockersupervisor.NewCapabilityProbeContract(bound.ProbeImageID, bound.SandboxPolicyDigest)
	if err != nil || contract.Digest() != expectedContract.Digest() || qualification.Digest() != active.QualificationDigest ||
		active.Probe.QualificationDigest != active.QualificationDigest || !qualification.ValidFor(identity, contract) {
		return localweb.ProductOptions{}, errors.New("installed Engine qualification does not match active release")
	}
	if active.Probe.Engine.DaemonID != identity.DaemonID() || active.Probe.Engine.APIVersion != identity.APIVersion() ||
		active.Probe.Engine.OperatingSystem != identity.OperatingSystem() || active.Probe.Engine.Architecture != identity.Architecture() ||
		active.Probe.Engine.ContextName != identity.ContextName() || active.Probe.Engine.EndpointDigest != identity.ContextEndpointDigest() {
		return localweb.ProductOptions{}, errors.New("installed Engine observation changed")
	}
	selection, err := resolver.InspectCompatiblePATH(ctx)
	if err != nil && hasPrivatePi {
		selection, err = resolver.SelectExisting(ctx)
	}
	if err != nil && !hasPrivatePi && input.AllowPrivatePiInstall {
		// PATH was compatible at setup, so no private bytes were installed.
		// A later PATH drift installs only the authenticated exact fallback at
		// service-start, never during Attempt preparation.
		selection, err = resolver.InstallPrivate(ctx)
	}
	if err != nil {
		return localweb.ProductOptions{}, errors.New("installed Pi selection is unavailable")
	}
	keepLedger = true
	return localweb.ProductOptions{
		InstallationStateRoot: input.StateRoot, GenerationID: input.GenerationID,
		ActivatedRelease: &release, ActivatedReleaseRoot: input.Release.Root,
		DockerRunner: operationLedger, DockerLedgerOwner: localweb.NewDockerOperationLedgerOwnership(operationLedger), DockerCapability: contract,
		DockerQualification: qualification, LocalPiResolver: resolver, LocalPiSelection: selection,
	}, nil
}
