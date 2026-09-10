package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
	"github.com/Yangyang96/chora/internal/sourcebundle"
)

const installedDoctorSchema = "chora.installed-product-proof/v1"

const (
	setupReceiptSchema          = "chora.m1-o4-candidate-phase-receipt.v1"
	modelRequestAuthoritySchema = "chora.m1-o4-model-request-authority.v2"
)

type installedDoctorInputBinding struct {
	AuthFileSHA256 string `json:"auth_file_sha256"`
	CAFileSHA256   string `json:"ca_file_sha256"`
	ProxyURLSHA256 string `json:"proxy_url_sha256"`
	ModelURLSHA256 string `json:"model_url_sha256"`
}

type installedDoctorRoleImage struct {
	Role          string `json:"role"`
	ArtifactID    string `json:"artifact_id"`
	ConfigImageID string `json:"config_image_id"`
	ArchiveSHA256 string `json:"archive_sha256"`
	ArchiveSize   int64  `json:"archive_size"`
	PolicySHA256  string `json:"policy_sha256"`
}

type installedDoctorProof struct {
	SchemaVersion        string                                         `json:"schema_version"`
	Status               string                                         `json:"status"`
	GenerationID         string                                         `json:"generation_id"`
	ReleaseID            string                                         `json:"release_id"`
	ManifestSHA256       string                                         `json:"manifest_sha256"`
	SetupReceiptSHA256   string                                         `json:"setup_receipt_sha256"`
	ModelAuthoritySHA256 string                                         `json:"model_request_authority_sha256"`
	DockerReadOnly       bool                                           `json:"docker_operations_read_only"`
	EngineMutations      int                                            `json:"engine_mutations_attempted"`
	ResourcesCreated     bool                                           `json:"resources_created"`
	ModelAuthenticated   bool                                           `json:"model_authenticated"`
	InputBinding         installedDoctorInputBinding                    `json:"input_binding"`
	EngineIdentity       dockersupervisor.EngineIdentityRecord          `json:"engine_identity"`
	CapabilityContract   dockersupervisor.CapabilityProbeContractRecord `json:"capability_contract"`
	EngineQualification  dockersupervisor.EngineQualificationRecord     `json:"engine_qualification"`
	RoleImages           []installedDoctorRoleImage                     `json:"role_images"`
	Doctor               preflight.Report                               `json:"doctor"`
}

type installedDoctorCommand struct {
	Runtime              installedRuntimeInput
	Config               preflight.Config
	SetupReceiptPath     string
	SetupReceiptSHA256   string
	ModelAuthorityPath   string
	ModelAuthoritySHA256 string
}

type setupReceiptAuthority struct {
	SchemaVersion         string  `json:"schemaVersion"`
	TupleIdentity         string  `json:"tupleIdentity"`
	Phase                 string  `json:"phase"`
	Sequence              int     `json:"sequence"`
	Status                string  `json:"status"`
	OperationDigest       string  `json:"operationDigest"`
	ObservationDigest     *string `json:"observationDigest"`
	PreviousReceiptDigest *string `json:"previousReceiptDigest"`
	ReceiptDigest         string  `json:"receiptDigest"`
}

type modelRequestAuthority struct {
	SchemaVersion          string `json:"schemaVersion"`
	Status                 string `json:"status"`
	GenerationID           string `json:"generationId"`
	ProviderIdentitySHA256 string `json:"providerIdentitySha256"`
	ModelIdentitySHA256    string `json:"modelIdentitySha256"`
	PolicySHA256           string `json:"policySha256"`
	ProxyURLSHA256         string `json:"proxyURLSha256"`
	ModelURLSHA256         string `json:"modelURLSha256"`
	AuthFileSHA256         string `json:"authFileSha256"`
	CAFileSHA256           string `json:"caFileSha256"`
	AuthenticationRequired bool   `json:"authenticationRequired"`
}

type installedDoctorAuthorities struct {
	Setup setupReceiptAuthority
	Model modelRequestAuthority
}

// qualifiedInstalledPreflightProbes binds provider-neutral preflight to the
// exact Runner and immutable qualification restored from the active installed
// generation. Preflight re-observes the same Runner again before using it; no
// current/default Docker context or provider-shaped output is synthesized.
func qualifiedInstalledPreflightProbes(
	ctx context.Context,
	base preflight.Probes,
	runner dockersupervisor.CommandRunner,
	contract dockersupervisor.CapabilityProbeContract,
	qualification dockersupervisor.EngineQualification,
	selection pidistribution.Selection,
	release *releaseassets.ActivatedRelease,
	generationID, manifestSHA256 string,
) (preflight.Probes, error) {
	if runner == nil || !selection.Configured() || release == nil || !productIdentifier.MatchString(generationID) || !validFingerprint(manifestSHA256) {
		return preflight.Probes{}, errors.New("installed runtime evidence unavailable")
	}
	managed, err := release.ImageForRole(releaseassets.RoleManagedPiRuntime)
	if err != nil {
		return preflight.Probes{}, errors.New("installed managed image evidence unavailable")
	}
	verifier, err := release.ImageForRole(releaseassets.RoleIndependentVerifier)
	if err != nil {
		return preflight.Probes{}, errors.New("installed verifier image evidence unavailable")
	}
	boundary, err := release.ImageForRole(releaseassets.RoleNetworkBoundary)
	if err != nil {
		return preflight.Probes{}, errors.New("installed boundary image evidence unavailable")
	}
	identity, err := dockersupervisor.ObserveEngine(ctx, runner)
	if err != nil || !qualification.ValidFor(identity, contract) {
		return preflight.Probes{}, errors.New("installed Engine qualification changed")
	}
	base.QualifiedRuntime = &preflight.QualifiedRuntime{
		Runner: runner, Identity: identity, Contract: contract,
		Qualification: qualification, PiSelection: selection,
		ManagedImageID: managed.LocalDockerConfigImageID, VerifierImageID: verifier.LocalDockerConfigImageID,
		BoundaryImageID: boundary.LocalDockerConfigImageID, GenerationID: generationID,
		ReleaseID: managed.ReleaseID, ManifestSHA256: manifestSHA256,
	}
	return base, nil
}

func runInstalledDoctor(args []string, stdout, stderr io.Writer, probes preflight.Probes) int {
	command, err := parseInstalledDoctorCommand(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "installed-doctor requires exact installed generation, release, Engine, Pi, and Preflight inputs")
		return 2
	}
	proof, err := proveInstalledProduct(context.Background(), command, probes)
	if err != nil {
		fmt.Fprintln(stderr, "installed product proof unavailable")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(proof); err != nil {
		fmt.Fprintln(stderr, "installed product proof output failed")
		return 2
	}
	if proof.Status != preflight.StatusPassed {
		return 1
	}
	return 0
}

func proveInstalledProduct(ctx context.Context, command installedDoctorCommand, probes preflight.Probes) (installedDoctorProof, error) {
	authorities, err := validateInstalledDoctorAuthorities(command)
	if err != nil {
		return installedDoctorProof{}, err
	}
	options, err := loadInstalledRuntime(ctx, command.Runtime)
	if err != nil {
		return installedDoctorProof{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = options.CloseOwnedDockerOperationLedger()
		}
	}()
	qualified, err := qualifiedInstalledPreflightProbes(ctx, probes, options.DockerRunner, options.DockerCapability,
		options.DockerQualification, options.LocalPiSelection, options.ActivatedRelease,
		command.Runtime.GenerationID, command.Runtime.Release.ManifestSHA256)
	if err != nil || qualified.QualifiedRuntime == nil {
		return installedDoctorProof{}, errors.New("installed runtime preflight evidence unavailable")
	}
	command.Config.Mode = preflight.ModeInstalledDoctor
	report := preflight.Run(ctx, command.Config, qualified)
	inputBinding := installedDoctorInputBinding{}
	if report.InputEvidence != nil {
		inputBinding = installedDoctorInputBinding{
			AuthFileSHA256: report.InputEvidence.AuthFileSHA256, CAFileSHA256: report.InputEvidence.CAFileSHA256,
			ProxyURLSHA256: report.InputEvidence.ProxyURLSHA256, ModelURLSHA256: report.InputEvidence.ModelURLSHA256,
		}
	}
	if report.Status == preflight.StatusPassed && (!validFingerprint(inputBinding.AuthFileSHA256) || !validFingerprint(inputBinding.CAFileSHA256) ||
		!validFingerprint(inputBinding.ProxyURLSHA256) || !validFingerprint(inputBinding.ModelURLSHA256)) {
		return installedDoctorProof{}, errors.New("installed Doctor input identity unavailable")
	}
	if report.Status == preflight.StatusPassed && (authorities.Model.AuthFileSHA256 != inputBinding.AuthFileSHA256 ||
		authorities.Model.CAFileSHA256 != inputBinding.CAFileSHA256 || authorities.Model.ProxyURLSHA256 != inputBinding.ProxyURLSHA256 ||
		authorities.Model.ModelURLSHA256 != inputBinding.ModelURLSHA256) {
		return installedDoctorProof{}, errors.New("installed Doctor model authority does not bind observed inputs")
	}
	roles := []string{releaseassets.RoleManagedPiRuntime, releaseassets.RoleNetworkBoundary, releaseassets.RoleIndependentVerifier, releaseassets.RoleCapabilityProbe}
	roleImages := make([]installedDoctorRoleImage, 0, len(roles))
	for _, role := range roles {
		image, imageErr := options.ActivatedRelease.ImageForRole(role)
		if imageErr != nil {
			return installedDoctorProof{}, errors.New("installed release role evidence unavailable")
		}
		roleImages = append(roleImages, installedDoctorRoleImage{
			Role: image.Role, ArtifactID: image.ArtifactID, ConfigImageID: image.LocalDockerConfigImageID,
			ArchiveSHA256: image.ArchiveSHA256, ArchiveSize: image.ArchiveSize, PolicySHA256: image.PolicySHA256,
		})
	}
	proof := installedDoctorProof{
		SchemaVersion: installedDoctorSchema, Status: report.Status, GenerationID: command.Runtime.GenerationID,
		ReleaseID: command.Runtime.Release.ReleaseID, ManifestSHA256: command.Runtime.Release.ManifestSHA256,
		SetupReceiptSHA256: command.SetupReceiptSHA256, ModelAuthoritySHA256: command.ModelAuthoritySHA256,
		DockerReadOnly: true, EngineMutations: 0, ResourcesCreated: report.ResourcesCreated,
		ModelAuthenticated: report.Status == preflight.StatusPassed && report.ModelProbe != nil && len(report.ModelProbe.Attempts) > 0,
		InputBinding:       inputBinding,
		EngineIdentity:     qualified.QualifiedRuntime.Identity.Record(), CapabilityContract: options.DockerCapability.Record(),
		EngineQualification: options.DockerQualification.Record(), RoleImages: roleImages, Doctor: report,
	}
	if err := options.CloseOwnedDockerOperationLedger(); err != nil {
		return installedDoctorProof{}, errors.New("installed Docker proof ledger close failed")
	}
	closed = true
	return proof, nil
}

func validateInstalledDoctorAuthorities(command installedDoctorCommand) (installedDoctorAuthorities, error) {
	if command.Config.SetupReceiptSHA256 != command.SetupReceiptSHA256 || command.Config.ModelAuthoritySHA256 != command.ModelAuthoritySHA256 {
		return installedDoctorAuthorities{}, errors.New("installed Doctor authority fingerprint binding changed")
	}
	setupBytes, err := readExactAuthority0400(command.SetupReceiptPath, command.SetupReceiptSHA256)
	if err != nil {
		return installedDoctorAuthorities{}, errors.New("exact Setup receipt unavailable")
	}
	var setup setupReceiptAuthority
	if err := decodeExactJSON(setupBytes, &setup); err != nil || setup.SchemaVersion != setupReceiptSchema ||
		setup.Status != "passed" || setup.Phase != "setup" || setup.Sequence < 1 || setup.Sequence > 16 ||
		!validFingerprint(setup.TupleIdentity) || !validFingerprint(setup.OperationDigest) || setup.ObservationDigest == nil ||
		!validFingerprint(*setup.ObservationDigest) || setup.PreviousReceiptDigest == nil || !validFingerprint(*setup.PreviousReceiptDigest) ||
		!validFingerprint(setup.ReceiptDigest) || setup.ReceiptDigest != setupReceiptDigest(setup) {
		return installedDoctorAuthorities{}, errors.New("Setup receipt identity is invalid")
	}
	modelBytes, err := readExactAuthority0400(command.ModelAuthorityPath, command.ModelAuthoritySHA256)
	if err != nil {
		return installedDoctorAuthorities{}, errors.New("exact model request authority unavailable")
	}
	var model modelRequestAuthority
	if err := decodeExactJSON(modelBytes, &model); err != nil || model.SchemaVersion != modelRequestAuthoritySchema || model.Status != "authorized" ||
		model.GenerationID != command.Runtime.GenerationID || !model.AuthenticationRequired ||
		!validFingerprint(model.ProviderIdentitySHA256) || !validFingerprint(model.ModelIdentitySHA256) || !validFingerprint(model.PolicySHA256) ||
		!validFingerprint(model.ProxyURLSHA256) || !validFingerprint(model.ModelURLSHA256) ||
		!validFingerprint(model.AuthFileSHA256) || !validFingerprint(model.CAFileSHA256) ||
		model.ProxyURLSHA256 != hashText(command.Config.ProxyURL) || model.ModelURLSHA256 != hashText(command.Config.ModelURL) {
		return installedDoctorAuthorities{}, errors.New("model request authority identity is invalid")
	}
	return installedDoctorAuthorities{Setup: setup, Model: model}, nil
}

func readExactAuthority0400(path, expectedSHA256 string) ([]byte, error) {
	if !canonicalAbsolute(path) || !validFingerprint(expectedSHA256) {
		return nil, errors.New("invalid authority binding")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || info.Size() < 2 || info.Size() > 1<<20 {
		return nil, errors.New("authority file identity is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("authority file ownership is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("authority file path is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o400 ||
		!openedOK || openedStat.Uid != uint32(os.Geteuid()) || openedStat.Nlink != 1 || opened.Size() != info.Size() {
		return nil, errors.New("authority file identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || hashBytes(data) != expectedSHA256 {
		return nil, errors.New("authority file digest changed")
	}
	return data, nil
}

func decodeExactJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("authority JSON has trailing content")
	}
	return nil
}

func setupReceiptDigest(receipt setupReceiptAuthority) string {
	safe := map[string]any{
		"schemaVersion": receipt.SchemaVersion, "tupleIdentity": receipt.TupleIdentity, "phase": receipt.Phase,
		"sequence": receipt.Sequence, "status": receipt.Status, "operationDigest": receipt.OperationDigest,
		"observationDigest": receipt.ObservationDigest, "previousReceiptDigest": receipt.PreviousReceiptDigest,
	}
	encoded, _ := json.Marshal(safe)
	return hashBytes(encoded)
}

func hashText(value string) string { return hashBytes([]byte(value)) }

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func parseInstalledDoctorCommand(args []string, stderr io.Writer) (installedDoctorCommand, error) {
	flags := flag.NewFlagSet("installed-doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateRoot := flags.String("state-root", "", "owner-private installation state root")
	generation := flags.String("generation", "", "exact active generation")
	dockerCLI := flags.String("docker-cli", "", "exact Docker CLI")
	dockerContext := flags.String("docker-context", "", "explicit Docker context")
	endpointDigest := flags.String("endpoint-digest", "", "explicit endpoint digest")
	releaseRoot := flags.String("release-root", "", "installed release root")
	releaseManifest := flags.String("release-manifest", "", "root-relative release manifest")
	manifestSHA := flags.String("manifest-sha256", "", "release manifest digest")
	releaseID := flags.String("release-id", "", "release identity")
	privatePiRoot := flags.String("private-pi-root", "", "owner-private Pi installation root")
	privatePiSource := flags.String("private-pi-source", "", "authenticated private Pi source root")
	privatePiManifest := flags.String("private-pi-manifest", "", "authenticated private Pi manifest")
	privatePiManifestSHA := flags.String("private-pi-manifest-sha256", "", "private Pi manifest digest")
	sourceRoot := flags.String("source", "", "immutable source root")
	sourceManifest := flags.String("source-manifest", "", "immutable source manifest")
	bundleAggregate := flags.String("bundle-aggregate", "", "source bundle aggregate")
	installRoot := flags.String("install", "", "installed product root")
	dataRoot := flags.String("data", "", "installed data root")
	authFile := flags.String("auth", "", "owner-only OAuth file")
	caFile := flags.String("ca", "", "pinned enterprise CA")
	proxyURL := flags.String("proxy", "", "fixed enterprise proxy")
	modelURL := flags.String("model-url", "", "allowlisted model URL")
	port := flags.Int("port", 0, "loopback service port")
	setupReceipt := flags.String("setup-receipt", "", "exact owner-read-only Setup phase receipt")
	setupReceiptSHA := flags.String("setup-receipt-sha256", "", "Setup phase receipt digest")
	modelAuthority := flags.String("model-request-authority", "", "exact owner-read-only static model request authority")
	modelAuthoritySHA := flags.String("model-request-authority-sha256", "", "static model request authority digest")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return installedDoctorCommand{}, errors.New("invalid installed Doctor arguments")
	}
	if !canonicalAbsolute(*stateRoot) || !productIdentifier.MatchString(*generation) ||
		!canonicalAbsolute(*dockerCLI) || !productIdentifier.MatchString(*dockerContext) || !validFingerprint(*endpointDigest) ||
		!canonicalAbsolute(*releaseRoot) || *releaseManifest == "" || filepath.IsAbs(*releaseManifest) || filepath.Clean(*releaseManifest) != *releaseManifest ||
		!validFingerprint(*manifestSHA) || !productIdentifier.MatchString(*releaseID) ||
		!canonicalAbsolute(*privatePiRoot) || !canonicalAbsolute(*privatePiSource) || !canonicalAbsolute(*privatePiManifest) || !validFingerprint(*privatePiManifestSHA) ||
		!canonicalAbsolute(*sourceRoot) || *sourceRoot != *installRoot ||
		!canonicalAbsolute(*sourceManifest) || *sourceManifest != filepath.Join(*installRoot, sourcebundle.ManifestName) || !validFingerprint(*bundleAggregate) ||
		!canonicalAbsolute(*installRoot) || !canonicalAbsolute(*dataRoot) || !canonicalAbsolute(*authFile) || !canonicalAbsolute(*caFile) ||
		*proxyURL != preflight.FixedProxyURL || *modelURL != preflight.AllowedModelURL || *port < 1 || *port > 65535 ||
		!canonicalAbsolute(*setupReceipt) || !validFingerprint(*setupReceiptSHA) ||
		!canonicalAbsolute(*modelAuthority) || !validFingerprint(*modelAuthoritySHA) {
		return installedDoctorCommand{}, errors.New("invalid installed Doctor identity")
	}
	privatePi := productCommand{privatePiRoot: *privatePiRoot, privatePiSource: *privatePiSource,
		privatePiManifest: *privatePiManifest, privatePiManifestSHA256: *privatePiManifestSHA}
	return installedDoctorCommand{
		Runtime: installedRuntimeInput{StateRoot: *stateRoot, GenerationID: *generation,
			Target:    productinstall.EngineTarget{CLIPath: *dockerCLI, ContextName: *dockerContext, EndpointDigest: *endpointDigest},
			Release:   releaseassets.ActivationInput{Root: *releaseRoot, ManifestPath: *releaseManifest, ManifestSHA256: *manifestSHA, ReleaseID: *releaseID},
			PrivatePi: privatePi, AllowPrivatePiInstall: false},
		Config: preflight.Config{Deadline: 60 * time.Second, SourceRoot: *sourceRoot, SourceManifest: *sourceManifest,
			BundleAggregate: *bundleAggregate, InstallRoot: *installRoot, DataRoot: *dataRoot, AuthFile: *authFile,
			CAFile: *caFile, ProxyURL: *proxyURL, ModelURL: *modelURL, Port: *port, Mode: preflight.ModeInstalledDoctor,
			SetupReceiptSHA256: *setupReceiptSHA, ModelAuthoritySHA256: *modelAuthoritySHA},
		SetupReceiptPath: *setupReceipt, SetupReceiptSHA256: *setupReceiptSHA,
		ModelAuthorityPath: *modelAuthority, ModelAuthoritySHA256: *modelAuthoritySHA,
	}, nil
}
