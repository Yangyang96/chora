package productinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

const dockerImageKind = "oci_image"

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// ExactDockerAssets is the sole Docker command adapter used by observation,
// inspection, acquisition, verification, and removal. Its Runner is already
// frozen to one explicit context/endpoint authority by dockersupervisor.
type ExactDockerAssets struct {
	runner dockersupervisor.CommandRunner
	target EngineTarget
}

func NewExactDockerAssets(runner dockersupervisor.CommandRunner, target EngineTarget) (*ExactDockerAssets, error) {
	if runner == nil || validateTarget(target) != nil {
		return nil, ErrInvalidRequest
	}
	return &ExactDockerAssets{runner: runner, target: target}, nil
}

func (adapter *ExactDockerAssets) Observe(ctx context.Context, target EngineTarget) (EngineObservation, error) {
	if err := adapter.requireTarget(target); err != nil {
		return EngineObservation{}, err
	}
	identity, err := dockersupervisor.ObserveEngine(ctx, adapter.runner)
	if err != nil {
		return EngineObservation{}, errors.New("explicit Engine observation failed")
	}
	observation := engineObservation(identity)
	if err := validateObservation(target, observation); err != nil {
		return EngineObservation{}, errors.New("explicit Engine identity mismatch")
	}
	return observation, nil
}

func (adapter *ExactDockerAssets) InspectExact(ctx context.Context, target EngineTarget, asset AssetSpec) (AssetStatus, error) {
	if err := adapter.requireDockerAsset(target, asset); err != nil {
		return AssetStatus{}, err
	}
	result, err := adapter.run(ctx, dockersupervisor.Command{Args: []string{"image", "inspect", "--format", "{{json .}}", asset.Identity}})
	if err != nil || result.ExitCode != 0 {
		if dockerImageAbsent(result.Stderr) {
			return AssetStatus{}, nil
		}
		return AssetStatus{}, errors.New("inspect exact manifest-bound image failed")
	}
	identity, operatingSystem, architecture, err := decodeDockerImageInspection(result.Stdout)
	if err != nil || identity != asset.Identity || operatingSystem != "linux" || architecture != "arm64" {
		return AssetStatus{}, errors.New("inspect exact manifest-bound image returned invalid identity or platform")
	}
	return AssetStatus{Present: true, Identity: identity}, nil
}

func (adapter *ExactDockerAssets) AcquirePinned(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	if err := adapter.requireDockerAsset(target, asset); err != nil || asset.Ownership != OwnershipChora || asset.AcquisitionKind != AcquisitionLocalDockerArchive {
		return ErrInvalidRequest
	}
	archive, err := releaseassets.OpenValidatedDockerArchive(releaseassets.ActivatedImage{
		ArchiveFormat: releaseassets.DockerArchiveFormat,
		ArchivePath:   asset.ArchivePath, ArchiveSize: asset.ArchiveSize, ArchiveSHA256: asset.ArchiveSHA256,
		LocalDockerConfigImageID: asset.Identity,
	})
	if err != nil {
		return errors.New("open exact validated local Docker archive failed")
	}
	defer archive.Close()
	loadContext := dockersupervisor.WithOperationSafeTargetSHA256(ctx, asset.ArchiveSHA256)
	result, err := adapter.run(loadContext, dockersupervisor.Command{Args: []string{"image", "load", "--quiet"}, Stdin: archive})
	if err != nil || result.ExitCode != 0 {
		return errors.New("load exact validated local Docker archive failed")
	}
	status, err := adapter.InspectExact(ctx, target, asset)
	if err != nil || !status.Present || status.Identity != asset.Identity {
		return ErrAssetConflict
	}
	return nil
}

func (adapter *ExactDockerAssets) RemoveExact(ctx context.Context, target EngineTarget, asset AssetSpec) error {
	if err := adapter.requireDockerAsset(target, asset); err != nil || asset.Ownership != OwnershipChora {
		return ErrInvalidRequest
	}
	result, err := adapter.run(ctx, dockersupervisor.Command{Args: []string{"image", "rm", asset.Identity}})
	if err != nil || result.ExitCode != 0 {
		if dockerImageAbsent(result.Stderr) {
			return nil
		}
		return errors.New("remove exact Chora-owned image failed")
	}
	return nil
}

func (adapter *ExactDockerAssets) Verify(ctx context.Context, target EngineTarget, generation GenerationSpec, assets []InstalledAsset, probe ProbeResult) error {
	if err := adapter.requireTarget(target); err != nil || validateGeneration(generation) != nil ||
		!probe.Passed || !probe.CleanupProven || validateObservation(target, probe.Engine) != nil {
		return ErrInvalidRequest
	}
	if len(assets) != len(generation.Assets) {
		return ErrAssetConflict
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

func (adapter *ExactDockerAssets) requireTarget(target EngineTarget) error {
	if adapter == nil || adapter.runner == nil || target != adapter.target || validateTarget(target) != nil {
		return ErrInvalidRequest
	}
	return nil
}

func (adapter *ExactDockerAssets) requireDockerAsset(target EngineTarget, asset AssetSpec) error {
	if err := adapter.requireTarget(target); err != nil || validateAsset(asset) != nil || asset.Kind != dockerImageKind {
		return ErrInvalidRequest
	}
	return nil
}

func (adapter *ExactDockerAssets) run(ctx context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	if !allowedDockerAssetCommand(command) {
		return dockersupervisor.CommandResult{ExitCode: -1}, ErrInvalidRequest
	}
	command.Args = slices.Clone(command.Args)
	return adapter.runner.Run(ctx, command)
}

func allowedDockerAssetCommand(command dockersupervisor.Command) bool {
	args := command.Args
	if len(args) == 3 && args[0] == "image" && args[1] == "load" && args[2] == "--quiet" {
		return command.Stdin != nil && command.Stdout == nil && command.Stderr == nil
	}
	if len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" && args[3] == "{{json .}}" {
		return command.Stdin == nil && validAssetIdentity(args[4])
	}
	return command.Stdin == nil && len(args) == 3 && args[0] == "image" && args[1] == "rm" && validAssetIdentity(args[2])
}

func dockerImageAbsent(stderr []byte) bool {
	text := strings.ToLower(string(stderr))
	return strings.Contains(text, "no such image") || strings.Contains(text, "no such object")
}

func decodeDockerImageInspection(data []byte) (string, string, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
	}
	if err := decoder.Decode(&value); err != nil {
		return "", "", "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", "", "", errors.New("trailing Docker output")
	}
	return strings.TrimSpace(value.ID), strings.TrimSpace(value.OS), strings.TrimSpace(value.Architecture), nil
}

type DockerCapabilityProber struct {
	target   EngineTarget
	identity dockersupervisor.EngineIdentity
	contract dockersupervisor.CapabilityProbeContract
	probe    *dockersupervisor.CapabilityProbe
}

func NewDockerCapabilityProber(ctx context.Context, runner dockersupervisor.CommandRunner, target EngineTarget, runtimeRoot, probeImageID, policyDigest string) (*DockerCapabilityProber, error) {
	if runner == nil || validateTarget(target) != nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot {
		return nil, ErrInvalidRequest
	}
	identity, err := dockersupervisor.ObserveEngine(ctx, runner)
	if err != nil || identity.ContextName() != target.ContextName || identity.ContextEndpointDigest() != target.EndpointDigest {
		return nil, errors.New("qualifying Engine identity is unavailable")
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract(probeImageID, policyDigest)
	if err != nil {
		return nil, errors.New("capability Probe contract is invalid")
	}
	probe, err := dockersupervisor.NewCapabilityProbe(dockersupervisor.CapabilityProbeConfig{
		Runner: runner, RuntimeRoot: runtimeRoot, Identity: identity, Contract: contract,
	})
	if err != nil {
		return nil, errors.New("capability Probe is unavailable")
	}
	return &DockerCapabilityProber{target: target, identity: identity, contract: contract, probe: probe}, nil
}

func (adapter *DockerCapabilityProber) Probe(ctx context.Context, authority MutationAuthority, target EngineTarget, _ GenerationSpec) (ProbeResult, error) {
	if adapter == nil || adapter.probe == nil || target != adapter.target || validateAuthority(authority, authority.Action) != nil {
		return ProbeResult{}, ErrAuthorityRequired
	}
	qualification, err := adapter.probe.Probe(ctx, dockersupervisor.AuthorizeCapabilityProbe())
	if err != nil || !qualification.ValidFor(adapter.identity, adapter.contract) {
		return ProbeResult{}, errors.New("capability Probe failed")
	}
	contractJSON, contractErr := json.Marshal(adapter.contract)
	qualificationJSON, qualificationErr := json.Marshal(qualification)
	if contractErr != nil || qualificationErr != nil {
		return ProbeResult{}, errors.New("capability qualification serialization failed")
	}
	return ProbeResult{
		Passed: true, CleanupProven: true, QualificationDigest: qualification.Digest(),
		Engine: engineObservation(adapter.identity), CapabilityContract: contractJSON, EngineQualification: qualificationJSON,
	}, nil
}

func engineObservation(identity dockersupervisor.EngineIdentity) EngineObservation {
	return EngineObservation{
		DaemonID: identity.DaemonID(), APIVersion: identity.APIVersion(),
		OperatingSystem: identity.OperatingSystem(), Architecture: identity.Architecture(),
		ContextName: identity.ContextName(), EndpointDigest: identity.ContextEndpointDigest(),
	}
}

type BoundRelease struct {
	Generation          GenerationSpec
	ProbeImageID        string
	SandboxPolicyDigest string
}

// BindActivatedRelease binds release-distributed image roles separately from
// the sandbox policy enforced by the installed binary. The capability-probe
// role policy describes how the probe runs; it is not the Sandbox policy whose
// digest the resulting Engine qualification authorizes.
func BindActivatedRelease(release releaseassets.ActivatedRelease, generationID, manifestSHA256, sandboxPolicyDigest string) (BoundRelease, error) {
	if !digestPattern.MatchString(sandboxPolicyDigest) {
		return BoundRelease{}, ErrInvalidRequest
	}
	roles := []string{
		releaseassets.RoleCapabilityProbe,
		releaseassets.RoleIndependentVerifier,
		releaseassets.RoleManagedPiRuntime,
		releaseassets.RoleNetworkBoundary,
	}
	images := make([]releaseassets.ActivatedImage, 0, len(roles))
	for _, role := range roles {
		image, err := release.ImageForRole(role)
		if err != nil {
			return BoundRelease{}, ErrInvalidRequest
		}
		images = append(images, image)
	}
	return bindActivatedImages(images, generationID, manifestSHA256, sandboxPolicyDigest)
}

func bindActivatedImages(images []releaseassets.ActivatedImage, generationID, manifestSHA256, sandboxPolicyDigest string) (BoundRelease, error) {
	roles := []string{
		releaseassets.RoleCapabilityProbe,
		releaseassets.RoleIndependentVerifier,
		releaseassets.RoleManagedPiRuntime,
		releaseassets.RoleNetworkBoundary,
	}
	if len(images) != len(roles) || !digestPattern.MatchString(sandboxPolicyDigest) {
		return BoundRelease{}, ErrInvalidRequest
	}
	assets := make([]AssetSpec, 0, len(roles))
	releaseID := ""
	bound := BoundRelease{SandboxPolicyDigest: sandboxPolicyDigest}
	for index, role := range roles {
		image := images[index]
		if image.Role != role || image.ArchiveFormat != releaseassets.DockerArchiveFormat ||
			image.ArchivePath == "" || image.ArchiveSize <= 0 || !digestPattern.MatchString(image.ArchiveSHA256) {
			return BoundRelease{}, ErrInvalidRequest
		}
		if releaseID == "" {
			releaseID = image.ReleaseID
		} else if releaseID != image.ReleaseID {
			return BoundRelease{}, ErrAssetConflict
		}
		assets = append(assets, AssetSpec{
			ID: role, Kind: dockerImageKind, Identity: image.LocalDockerConfigImageID,
			Ownership: OwnershipChora, AcquisitionKind: AcquisitionLocalDockerArchive,
			ArchivePath: image.ArchivePath, ArchiveSize: image.ArchiveSize, ArchiveSHA256: image.ArchiveSHA256,
		})
		if role == releaseassets.RoleCapabilityProbe {
			bound.ProbeImageID = image.LocalDockerConfigImageID
		}
	}
	bound.Generation = GenerationSpec{
		SchemaVersion: GenerationSchema, GenerationID: generationID, ReleaseID: releaseID,
		ManifestSHA256: manifestSHA256, Assets: assets,
	}
	if validateGeneration(bound.Generation) != nil {
		return BoundRelease{}, ErrInvalidRequest
	}
	return bound, nil
}

// ExplicitLocalAuthorizer accepts only the exact local actor/session/grant and
// action supplied by the command's explicit authorization flags.
type ExplicitLocalAuthorizer struct {
	GrantID   string
	ActorID   string
	SessionID string
}

func (authorizer ExplicitLocalAuthorizer) Authorize(_ context.Context, authority MutationAuthority, action Action) error {
	if authority.GrantID != authorizer.GrantID || authority.ActorID != authorizer.ActorID ||
		authority.SessionID != authorizer.SessionID || authority.Action != action {
		return ErrAuthorityRequired
	}
	return nil
}

// ReadOnlyEngineDoctor exposes Doctor without constructing mutation ports.
func ReadOnlyEngineDoctor(ctx context.Context, clock Clock, observer EngineObserver, target EngineTarget) (DoctorReport, error) {
	if clock == nil || observer == nil {
		return DoctorReport{}, ErrInvalidRequest
	}
	service := &Service{deps: Dependencies{Clock: clock, Observer: observer}}
	return service.Doctor(ctx, target)
}
