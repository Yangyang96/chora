package productinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

func TestBindActivatedImagesUsesExplicitSandboxPolicyNotProbeRolePolicy(t *testing.T) {
	managedID := "sha256:" + strings.Repeat("1", 64)
	boundaryID := "sha256:" + strings.Repeat("2", 64)
	probeRolePolicy := strings.Repeat("3", 64)
	sandboxPolicy := strings.Repeat("4", 64)
	roles := []string{
		releaseassets.RoleCapabilityProbe,
		releaseassets.RoleIndependentVerifier,
		releaseassets.RoleManagedPiRuntime,
		releaseassets.RoleNetworkBoundary,
	}
	images := make([]releaseassets.ActivatedImage, 0, len(roles))
	for _, role := range roles {
		identity := managedID
		artifact := "managed"
		if role == releaseassets.RoleNetworkBoundary {
			identity = boundaryID
			artifact = "boundary"
		}
		images = append(images, releaseassets.ActivatedImage{
			Role: role, ArtifactID: artifact, LocalDockerConfigImageID: identity,
			ArchiveFormat: releaseassets.DockerArchiveFormat, ArchivePath: "/private/release/" + artifact + ".tar",
			ArchiveSize: 1, ArchiveSHA256: strings.Repeat(identity[len(identity)-1:], 64),
			PolicySHA256: probeRolePolicy, ReleaseID: "release-1",
		})
	}
	bound, err := bindActivatedImages(images, "g1", strings.Repeat("b", 64), sandboxPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ProbeImageID != managedID || bound.SandboxPolicyDigest != sandboxPolicy || bound.SandboxPolicyDigest == probeRolePolicy {
		t.Fatalf("bound capability contract = image %q sandbox %q", bound.ProbeImageID, bound.SandboxPolicyDigest)
	}
	if len(bound.Generation.Assets) != 4 || bound.Generation.Assets[0].AcquisitionKind != AcquisitionLocalDockerArchive ||
		bound.Generation.Assets[0].ArchivePath != "/private/release/managed.tar" || bound.Generation.Assets[0].ArchiveSize != 1 ||
		bound.Generation.Assets[0].ArchiveSHA256 != strings.Repeat("1", 64) || bound.Generation.Assets[0].AcquisitionRef != "" {
		t.Fatalf("bound offline acquisition = %+v", bound.Generation.Assets)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract(bound.ProbeImageID, bound.SandboxPolicyDigest)
	if err != nil || contract.SandboxPolicyDigest() != sandboxPolicy {
		t.Fatalf("capability contract = %+v / %v", contract, err)
	}
}

func TestBindActivatedImagesRejectsInvalidSandboxPolicyDigest(t *testing.T) {
	if _, err := bindActivatedImages(nil, "g1", strings.Repeat("b", 64), strings.Repeat("A", 64)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid sandbox policy digest error = %v", err)
	}
}

func TestBindActivatedImagesRejectsArchiveFormatAndAliasBindingDrift(t *testing.T) {
	images := activatedImageBindingsFixture()
	images[0].ArchiveFormat = "oci-layout"
	if _, err := bindActivatedImages(images, "g1", strings.Repeat("b", 64), strings.Repeat("c", 64)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("archive format error = %v", err)
	}
	images = activatedImageBindingsFixture()
	images[1].ArchivePath = "/private/release/substituted.tar"
	if _, err := bindActivatedImages(images, "g1", strings.Repeat("b", 64), strings.Repeat("c", 64)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("alias archive binding drift error = %v", err)
	}
}

func activatedImageBindingsFixture() []releaseassets.ActivatedImage {
	roles := []string{releaseassets.RoleCapabilityProbe, releaseassets.RoleIndependentVerifier, releaseassets.RoleManagedPiRuntime, releaseassets.RoleNetworkBoundary}
	images := make([]releaseassets.ActivatedImage, 0, len(roles))
	for _, role := range roles {
		identity, artifact, digit := "sha256:"+strings.Repeat("1", 64), "managed", "1"
		if role == releaseassets.RoleNetworkBoundary {
			identity, artifact, digit = "sha256:"+strings.Repeat("2", 64), "boundary", "2"
		}
		images = append(images, releaseassets.ActivatedImage{
			Role: role, ArtifactID: artifact, LocalDockerConfigImageID: identity,
			ArchiveFormat: releaseassets.DockerArchiveFormat, ArchivePath: "/private/release/" + artifact + ".tar",
			ArchiveSize: 1, ArchiveSHA256: strings.Repeat(digit, 64), PolicySHA256: strings.Repeat("3", 64), ReleaseID: "release-1",
		})
	}
	return images
}

func TestFileBackendCASModesActivationAndSymlinkRejection(t *testing.T) {
	parent := canonicalTemporaryDirectory(t)
	root := filepath.Join(parent, "product-state")
	backend, err := NewFileBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("construction mutated filesystem: %v", err)
	}
	if err := backend.WithInstallationLock(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Join(root, fileLockName), 0o600)
	if err := backend.RequireReferenceSnapshot(context.Background()); !errors.Is(err, ErrGenerationReferenced) {
		t.Fatalf("missing reference snapshot error = %v", err)
	}
	initialReferences := GenerationReferences{ServingGenerationIDs: []string{"g0"}, ActiveAttemptGenerationIDs: []string{"g1"}, RecoverableAttemptGenerationIDs: []string{"g2"}}
	if err := backend.PublishReferences(context.Background(), initialReferences); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(root, fileReferencesName), 0o600)
	if err := backend.RequireReferenceSnapshot(context.Background()); err != nil {
		t.Fatalf("valid reference snapshot error = %v", err)
	}
	if err := backend.PublishReferences(context.Background(), GenerationReferences{ActiveAttemptGenerationIDs: []string{"g2", "g1"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsorted PublishReferences error = %v", err)
	}
	if observed, err := backend.References(context.Background()); err != nil || !slices.Equal(observed.ServingGenerationIDs, []string{"g0"}) || !slices.Equal(observed.ActiveAttemptGenerationIDs, []string{"g1"}) || !slices.Equal(observed.RecoverableAttemptGenerationIDs, []string{"g2"}) {
		t.Fatalf("failed publication changed references: %+v / %v", observed, err)
	}
	if err := backend.RetireServingGeneration(context.Background(), "g0"); err != nil {
		t.Fatal(err)
	}
	if observed, err := backend.References(context.Background()); err != nil || len(observed.ServingGenerationIDs) != 0 || !slices.Equal(observed.ActiveAttemptGenerationIDs, []string{"g1"}) || !slices.Equal(observed.RecoverableAttemptGenerationIDs, []string{"g2"}) {
		t.Fatalf("serving retirement changed Attempt references: %+v / %v", observed, err)
	}
	if err := os.Chmod(filepath.Join(root, fileReferencesName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.PublishReferences(context.Background(), GenerationReferences{}); err == nil {
		t.Fatal("unsafe existing reference file did not fail closed")
	}
	if err := os.Chmod(filepath.Join(root, fileReferencesName), 0o600); err != nil {
		t.Fatal(err)
	}
	if observed, err := backend.References(context.Background()); err != nil || !slices.Equal(observed.ActiveAttemptGenerationIDs, []string{"g1"}) {
		t.Fatalf("failed atomic replacement changed prior snapshot: %+v / %v", observed, err)
	}

	state := EmptyState()
	state.Revision = 1
	if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(root, fileStateName), 0o600)
	loaded, err := backend.Load(context.Background())
	if err != nil || loaded.Revision != 1 {
		t.Fatalf("Load() = (%+v, %v)", loaded, err)
	}
	state.Revision = 2
	if err := backend.SaveCAS(context.Background(), 0, state); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("stale SaveCAS error = %v", err)
	}

	spec := generationSpec("g1", ownedAsset("agent", "a"))
	if err := backend.AtomicActivate(context.Background(), "", spec); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(root, fileActivationName), 0o600)
	if current, err := backend.Current(context.Background()); err != nil || current != "g1" {
		t.Fatalf("Current() = (%q, %v)", current, err)
	}
	if err := backend.AtomicDeactivate(context.Background(), "g1"); err != nil {
		t.Fatal(err)
	}

	symlinkRoot := filepath.Join(parent, "state-link")
	if err := os.Symlink(root, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	symlinkBackend, err := NewFileBackend(symlinkRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := symlinkBackend.Load(context.Background()); err == nil {
		t.Fatal("symlink state root was accepted")
	}
	if err := symlinkBackend.WithInstallationLock(context.Background(), func(context.Context) error { return nil }); err == nil {
		t.Fatal("symlink lock root was accepted")
	}

	if err := os.Remove(filepath.Join(root, fileStateName)); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(parent, "external.json")
	if err := os.WriteFile(external, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, fileStateName)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Load(context.Background()); err == nil {
		t.Fatal("symlink state file was accepted")
	}
}

func TestFileBackendReferencesRejectLegacyAndNonExactServingSchemas(t *testing.T) {
	root := filepath.Join(canonicalTemporaryDirectory(t), "reference-schema")
	backend, err := NewFileBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		SchemaVersion                   string   `json:"schema_version"`
		ActiveAttemptGenerationIDs      []string `json:"active_attempt_generation_ids"`
		RecoverableAttemptGenerationIDs []string `json:"recoverable_attempt_generation_ids"`
	}{"chora.product-generation-references/v1", []string{"g1"}, nil}
	if err := backend.writeJSON(context.Background(), fileReferencesName, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.References(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("legacy reference schema error = %v", err)
	}
	if err := backend.RequireReferenceSnapshot(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("legacy destructive snapshot gate error = %v", err)
	}
	missingServing := struct {
		SchemaVersion                   string   `json:"schema_version"`
		ActiveAttemptGenerationIDs      []string `json:"active_attempt_generation_ids"`
		RecoverableAttemptGenerationIDs []string `json:"recoverable_attempt_generation_ids"`
	}{referenceSchema, []string{}, []string{}}
	if err := backend.writeJSON(context.Background(), fileReferencesName, missingServing); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.References(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("v2 snapshot missing serving field error = %v", err)
	}
	if err := backend.RequireReferenceSnapshot(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("v2 destructive snapshot gate missing serving field error = %v", err)
	}

	if err := backend.PublishReferences(context.Background(), GenerationReferences{ServingGenerationIDs: []string{"g2", "g1"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsorted serving references error = %v", err)
	}
	if err := backend.PublishReferences(context.Background(), GenerationReferences{ServingGenerationIDs: []string{"g1", "g1"}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate serving references error = %v", err)
	}
}

func TestFileBackendDurableReadsRejectValidTargetAliases(t *testing.T) {
	tests := []struct {
		name       string
		initialize func(*testing.T, *FileBackend)
		fileName   string
		read       func(*FileBackend) error
	}{
		{
			name: "state",
			initialize: func(t *testing.T, backend *FileBackend) {
				state := EmptyState()
				state.Revision = 1
				if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
					t.Fatal(err)
				}
			},
			fileName: fileStateName,
			read: func(backend *FileBackend) error {
				_, err := backend.Load(context.Background())
				return err
			},
		},
		{
			name: "activation",
			initialize: func(t *testing.T, backend *FileBackend) {
				if err := backend.AtomicActivate(context.Background(), "", generationSpec("g1", ownedAsset("agent", "a"))); err != nil {
					t.Fatal(err)
				}
			},
			fileName: fileActivationName,
			read: func(backend *FileBackend) error {
				_, err := backend.Current(context.Background())
				return err
			},
		},
		{
			name: "references",
			initialize: func(t *testing.T, backend *FileBackend) {
				if err := backend.PublishReferences(context.Background(), GenerationReferences{ActiveAttemptGenerationIDs: []string{"g1"}}); err != nil {
					t.Fatal(err)
				}
			},
			fileName: fileReferencesName,
			read: func(backend *FileBackend) error {
				_, err := backend.References(context.Background())
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := canonicalTemporaryDirectory(t)
			root := filepath.Join(parent, "product-state")
			backend, err := NewFileBackend(root)
			if err != nil {
				t.Fatal(err)
			}
			test.initialize(t, backend)
			path := filepath.Join(root, test.fileName)
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, test.name+"-valid-target.json")
			if err := os.WriteFile(target, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if err := test.read(backend); err == nil {
				t.Fatalf("valid-target %s symlink was accepted", test.name)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, path); err != nil {
				t.Fatal(err)
			}
			if err := test.read(backend); err == nil {
				t.Fatalf("owner-private %s hard link was accepted", test.name)
			}
		})
	}
}

func TestFileBackendLockSerializesIndependentInstances(t *testing.T) {
	root := filepath.Join(canonicalTemporaryDirectory(t), "product-state")
	first, _ := NewFileBackend(root)
	second, _ := NewFileBackend(root)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- first.WithInstallationLock(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	waitSignal(t, entered, "file lock holder")
	go func() {
		secondDone <- second.WithInstallationLock(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second backend escaped file lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := waitError(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := waitError(t, secondDone); err != nil {
		t.Fatal(err)
	}
}

func TestFileBackendReferencePublicationSerializesIndependentInstances(t *testing.T) {
	root := filepath.Join(canonicalTemporaryDirectory(t), "product-state")
	first, _ := NewFileBackend(root)
	second, _ := NewFileBackend(root)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- first.withFileLock(context.Background(), fileRefsLockName, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	waitSignal(t, entered, "reference publication lock holder")
	go func() {
		secondDone <- second.PublishReferences(context.Background(), GenerationReferences{ActiveAttemptGenerationIDs: []string{"g1"}})
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("reference publication escaped file lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := waitError(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := waitError(t, secondDone); err != nil {
		t.Fatal(err)
	}
	if references, err := second.References(context.Background()); err != nil || !slices.Equal(references.ActiveAttemptGenerationIDs, []string{"g1"}) {
		t.Fatalf("published references = %+v / %v", references, err)
	}
}

func TestFileBackendCASAllowsOnlyOneConcurrentWriter(t *testing.T) {
	root := filepath.Join(canonicalTemporaryDirectory(t), "product-state")
	first, _ := NewFileBackend(root)
	second, _ := NewFileBackend(root)
	state := EmptyState()
	state.Revision = 1
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, backend := range []*FileBackend{first, second} {
		go func(candidate *FileBackend) {
			<-start
			results <- candidate.SaveCAS(context.Background(), 0, state)
		}(backend)
	}
	close(start)
	errorsSeen := []error{waitError(t, results), waitError(t, results)}
	successes, conflicts := 0, 0
	for _, err := range errorsSeen {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConcurrentUpdate) {
			conflicts++
		} else {
			t.Fatalf("SaveCAS error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results = %+v", errorsSeen)
	}
}

func TestFileBackendRoundTripsStrictEngineQualificationEvidence(t *testing.T) {
	identity, err := dockersupervisor.NewEngineIdentity(dockersupervisor.EngineIdentityInput{
		DaemonID: "daemon-1", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: digest("e"), ProviderName: "Colima", EngineVersion: "29.6.1", ContextName: "chora-local",
	})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract("sha256:"+digest("a"), digest("b"))
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	record := dockersupervisor.EngineQualificationRecord{
		SchemaVersion: "chora.docker-engine-qualification/v1", EngineIdentityDigest: identity.Digest(),
		ProbeContractDigest: contract.Digest(), ProbeImageID: contract.ProbeImageID(),
		SandboxPolicyDigest: contract.SandboxPolicyDigest(), CompletedAt: completed,
	}
	payload, _ := json.Marshal(struct {
		SchemaVersion        string `json:"schema_version"`
		EngineIdentityDigest string `json:"engine_identity_digest"`
		ProbeContractDigest  string `json:"probe_contract_digest"`
		ProbeImageID         string `json:"probe_image_id"`
		SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
		CompletedAt          string `json:"completed_at"`
	}{record.SchemaVersion, record.EngineIdentityDigest, record.ProbeContractDigest, record.ProbeImageID, record.SandboxPolicyDigest, record.CompletedAt})
	record.QualificationDigest = fmt.Sprintf("%x", sha256.Sum256(payload))
	qualification, err := dockersupervisor.RestoreEngineQualification(record)
	if err != nil || !qualification.ValidFor(identity, contract) {
		t.Fatalf("qualification fixture = %v", err)
	}
	contractJSON, _ := json.Marshal(contract)
	qualificationJSON, _ := json.Marshal(qualification)
	target := EngineTarget{CLIPath: "/private/tools/docker", ContextName: "chora-local", EndpointDigest: digest("e")}
	spec := generationSpec("g1", ownedAsset("agent", "a"))
	generation := installedGeneration(spec, GenerationActive)
	generation.Target = target
	generation.Probe = ProbeResult{
		Passed: true, CleanupProven: true, QualificationDigest: qualification.Digest(),
		Engine: EngineObservation{DaemonID: identity.DaemonID(), APIVersion: identity.APIVersion(), OperatingSystem: identity.OperatingSystem(),
			Architecture: identity.Architecture(), ContextName: identity.ContextName(), EndpointDigest: identity.ContextEndpointDigest()},
		CapabilityContract: contractJSON, EngineQualification: qualificationJSON,
	}
	generation.QualificationDigest = qualification.Digest()
	state := EmptyState()
	state.Revision = 1
	state.ActiveGenerationID = "g1"
	state.Generations = []Generation{generation}
	root := filepath.Join(canonicalTemporaryDirectory(t), "qualified-state")
	backend, _ := NewFileBackend(root)
	if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := backend.Load(context.Background())
	if err != nil || len(loaded.Generations) != 1 {
		t.Fatalf("Load() = %+v / %v", loaded, err)
	}
	var restoredContract dockersupervisor.CapabilityProbeContract
	var restoredQualification dockersupervisor.EngineQualification
	probe := loaded.Generations[0].Probe
	if json.Unmarshal(probe.CapabilityContract, &restoredContract) != nil || json.Unmarshal(probe.EngineQualification, &restoredQualification) != nil ||
		!restoredQualification.ValidFor(identity, restoredContract) || restoredQualification.Digest() != loaded.Generations[0].QualificationDigest {
		t.Fatal("strict qualification evidence did not survive JSON state round-trip")
	}
}

func TestExactDockerAssetsDoctorIsReadOnlyAndSecretSafe(t *testing.T) {
	endpoint := "unix:///private/run/chora-docker.sock"
	endpointDigest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	target := EngineTarget{CLIPath: "/usr/local/bin/docker", ContextName: "chora-local", EndpointDigest: endpointDigest}
	runner := &traceDockerRunner{handler: engineObservationHandler(t, target.ContextName, endpoint)}
	adapter, err := NewExactDockerAssets(runner, target)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReadOnlyEngineDoctor(context.Background(), fakeClock{time.Now()}, adapter, target)
	if err != nil || !report.Ready || report.Engine.EndpointDigest != endpointDigest {
		t.Fatalf("Doctor() = (%+v, %v)", report, err)
	}
	for _, command := range runner.commands {
		joined := strings.Join(command, " ")
		for _, forbidden := range []string{" pull ", " rm ", "build", "load", "prune", "run", "create"} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("Doctor issued mutating command %q", joined)
			}
		}
	}

	hostile := &traceDockerRunner{handler: func([]string) (dockersupervisor.CommandResult, error) {
		return dockersupervisor.CommandResult{ExitCode: 1, Stderr: []byte("Bearer secret\n/private/daemon.sock")}, errors.New("private failure")
	}}
	adapter, _ = NewExactDockerAssets(hostile, target)
	report, err = ReadOnlyEngineDoctor(context.Background(), fakeClock{time.Now()}, adapter, target)
	if err != nil || report.Ready || report.Reason != DoctorReasonObservationUnavailable {
		t.Fatalf("hostile Doctor() = (%+v, %v)", report, err)
	}
	encoded, _ := json.Marshal(report)
	for _, secret := range []string{"Bearer", "secret", "/private/daemon.sock", "private failure"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("Doctor leaked %q in %s", secret, encoded)
		}
	}
}

func TestExactDockerAssetsUsesSameValidatedArchiveDescriptorAndExactInspectLoadRemove(t *testing.T) {
	target := EngineTarget{CLIPath: "/usr/local/bin/docker", ContextName: "chora-local", EndpointDigest: digest("e")}
	asset, archiveBytes := writeDockerArchiveAsset(t, "agent")
	present := false
	runner := &traceDockerRunner{commandHandler: func(command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
		args := command.Args
		switch {
		case slices.Equal(args, []string{"image", "inspect", "--format", "{{json .}}", asset.Identity}):
			if !present {
				return dockersupervisor.CommandResult{ExitCode: 1, Stderr: []byte("No such image")}, errors.New("exit 1")
			}
			return dockersupervisor.CommandResult{Stdout: []byte(fmt.Sprintf(`{"Id":%q,"Os":"linux","Architecture":"arm64"}`, asset.Identity))}, nil
		case slices.Equal(args, []string{"image", "load", "--quiet"}):
			file, ok := command.Stdin.(*os.File)
			if !ok || file.Name() != asset.ArchivePath {
				t.Fatalf("load stdin = %T %v", command.Stdin, command.Stdin)
			}
			if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
				t.Fatalf("validated archive offset = %d / %v", offset, err)
			}
			loaded, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(loaded, archiveBytes) {
				t.Fatalf("load stdin bytes = %d / %v", len(loaded), err)
			}
			present = true
			return dockersupervisor.CommandResult{}, nil
		case slices.Equal(args, []string{"image", "rm", asset.Identity}):
			present = false
			return dockersupervisor.CommandResult{}, nil
		default:
			t.Fatalf("unexpected Docker command: %q", args)
			return dockersupervisor.CommandResult{}, nil
		}
	}}
	adapter, _ := NewExactDockerAssets(runner, target)
	status, err := adapter.InspectExact(context.Background(), target, asset)
	if err != nil || status.Present {
		t.Fatalf("InspectExact() = (%+v, %v)", status, err)
	}
	if err := adapter.AcquirePinned(context.Background(), target, asset); err != nil {
		t.Fatal(err)
	}
	spec := generationSpec("g1", asset)
	probe := ProbeResult{Passed: true, CleanupProven: true, Engine: EngineObservation{
		DaemonID: "daemon-1", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextName: target.ContextName, EndpointDigest: target.EndpointDigest,
	}}
	if err := adapter.Verify(context.Background(), target, spec, []InstalledAsset{{Spec: asset}}, probe); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if err := adapter.RemoveExact(context.Background(), target, asset); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		joined := strings.Join(command, " ")
		for _, forbidden := range []string{"build", "pull", "prune", "tag", "context use", "context show", "default", "current"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("forbidden Docker authority in %q", joined)
			}
		}
	}
	for _, command := range []dockersupervisor.Command{{Args: []string{"build", "."}}, {Args: []string{"pull", "x"}}, {Args: []string{"load"}}, {Args: []string{"image", "load", "--quiet"}}, {Args: []string{"system", "prune"}}, {Args: []string{"image", "tag", asset.Identity, "latest"}}} {
		if allowedDockerAssetCommand(command) {
			t.Fatalf("forbidden command allowed: %q", command.Args)
		}
	}
}

func TestExactDockerAssetsRejectsPostLoadIdentityAndPlatformDrift(t *testing.T) {
	target := EngineTarget{CLIPath: "/usr/local/bin/docker", ContextName: "chora-local", EndpointDigest: digest("e")}
	for _, test := range []struct {
		name, id, os, arch string
	}{
		{name: "identity", id: "sha256:" + strings.Repeat("f", 64), os: "linux", arch: "arm64"},
		{name: "os", os: "darwin", arch: "arm64"},
		{name: "architecture", os: "linux", arch: "amd64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset, _ := writeDockerArchiveAsset(t, test.name)
			observedID := test.id
			if observedID == "" {
				observedID = asset.Identity
			}
			runner := &traceDockerRunner{commandHandler: func(command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
				if slices.Equal(command.Args, []string{"image", "load", "--quiet"}) {
					_, _ = io.Copy(io.Discard, command.Stdin)
					return dockersupervisor.CommandResult{}, nil
				}
				return dockersupervisor.CommandResult{Stdout: []byte(fmt.Sprintf(`{"Id":%q,"Os":%q,"Architecture":%q}`, observedID, test.os, test.arch))}, nil
			}}
			adapter, _ := NewExactDockerAssets(runner, target)
			if err := adapter.AcquirePinned(context.Background(), target, asset); !errors.Is(err, ErrAssetConflict) {
				t.Fatalf("AcquirePinned drift error = %v", err)
			}
		})
	}
}

func TestExactDockerAssetsRejectsArchiveBindingDriftBeforeDocker(t *testing.T) {
	target := EngineTarget{CLIPath: "/usr/local/bin/docker", ContextName: "chora-local", EndpointDigest: digest("e")}
	tests := []struct {
		name   string
		mutate func(*testing.T, *AssetSpec, []byte)
	}{
		{name: "size", mutate: func(_ *testing.T, asset *AssetSpec, _ []byte) { asset.ArchiveSize++ }},
		{name: "digest", mutate: func(_ *testing.T, asset *AssetSpec, _ []byte) { asset.ArchiveSHA256 = strings.Repeat("f", 64) }},
		{name: "config identity", mutate: func(_ *testing.T, asset *AssetSpec, _ []byte) { asset.Identity = "sha256:" + strings.Repeat("f", 64) }},
		{name: "bytes", mutate: func(t *testing.T, asset *AssetSpec, archive []byte) {
			if err := os.WriteFile(asset.ArchivePath, append(archive, 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, asset *AssetSpec, archive []byte) {
			targetPath := asset.ArchivePath + ".target"
			if err := os.WriteFile(targetPath, archive, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(asset.ArchivePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(targetPath, asset.ArchivePath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, asset *AssetSpec, _ []byte) {
			if err := os.Link(asset.ArchivePath, asset.ArchivePath+".link"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			asset, archive := writeDockerArchiveAsset(t, strings.ReplaceAll(test.name, " ", "-"))
			test.mutate(t, &asset, archive)
			runner := &traceDockerRunner{commandHandler: func(command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
				t.Fatalf("archive drift reached Docker: %q", command.Args)
				return dockersupervisor.CommandResult{}, nil
			}}
			adapter, _ := NewExactDockerAssets(runner, target)
			if err := adapter.AcquirePinned(context.Background(), target, asset); err == nil {
				t.Fatal("archive drift was accepted")
			}
			if len(runner.commands) != 0 {
				t.Fatalf("archive drift issued Docker commands: %+v", runner.commands)
			}
		})
	}
}

func TestPrivatePiUninstallJournalsInterruptedExactRemoval(t *testing.T) {
	world := newFakeWorld()
	binding := pidistribution.PrivateBinding{ManifestSHA256: digest("a"), ClosureSHA256: digest("b")}
	asset, err := privatePiAssetSpec(binding)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &fakePrivatePiLifecycle{binding: binding, present: true, interruptRemove: true}
	privatePi := &ExactPrivatePiAssets{target: world.target, binding: binding, lifecycle: lifecycle}
	spec := generationSpec("g1", asset)
	world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	service, err := New(Dependencies{
		Clock: world, Locker: world, Observer: world, Authorizer: world, Store: world,
		Inspector: privatePi, Acquirer: privatePi, Remover: privatePi, Prober: world,
		Verifier: world, Activator: world, References: world,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := UninstallRequest{Authority: authority(ActionUninstall), Key: "remove-private-pi", Target: world.target}
	if _, err := service.Uninstall(context.Background(), request); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("first Uninstall() error = %v", err)
	}
	if lifecycle.removeCalls != 1 || world.state.Operations[0].PendingAssetIdentity != binding.ClosureSHA256 {
		t.Fatalf("interrupted private removal = calls=%d state=%+v", lifecycle.removeCalls, world.state)
	}
	result, err := service.Uninstall(context.Background(), request)
	if err != nil || !result.Replayed || lifecycle.removeCalls != 2 || !slices.Equal(result.RemovedAssetIDs, []string{"private_pi"}) {
		t.Fatalf("resumed private Uninstall() = (%+v, %v), removals=%d", result, err, lifecycle.removeCalls)
	}
}

type fakePrivatePiLifecycle struct {
	binding         pidistribution.PrivateBinding
	present         bool
	interruptRemove bool
	removalPending  bool
	installCalls    int
	removeCalls     int
}

func (lifecycle *fakePrivatePiLifecycle) Inspect(context.Context) (pidistribution.PrivateBinding, bool, error) {
	return lifecycle.binding, lifecycle.present, nil
}

func (lifecycle *fakePrivatePiLifecycle) Install(context.Context) (pidistribution.PrivateBinding, error) {
	lifecycle.installCalls++
	lifecycle.present = true
	return lifecycle.binding, nil
}

func (lifecycle *fakePrivatePiLifecycle) RemovalPending(context.Context) (bool, error) {
	return lifecycle.removalPending, nil
}

func (lifecycle *fakePrivatePiLifecycle) Remove(context.Context) error {
	lifecycle.removeCalls++
	lifecycle.present = false
	if lifecycle.interruptRemove {
		lifecycle.interruptRemove = false
		lifecycle.removalPending = true
		return pidistribution.ErrRemovalInterrupted
	}
	lifecycle.removalPending = false
	return nil
}

type traceDockerRunner struct {
	mu             sync.Mutex
	commands       [][]string
	handler        func([]string) (dockersupervisor.CommandResult, error)
	commandHandler func(dockersupervisor.Command) (dockersupervisor.CommandResult, error)
}

func (runner *traceDockerRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, slices.Clone(command.Args))
	if runner.commandHandler != nil {
		return runner.commandHandler(command)
	}
	return runner.handler(command.Args)
}

func writeDockerArchiveAsset(t *testing.T, id string) (AssetSpec, []byte) {
	t.Helper()
	config := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` + strings.Repeat("5", 64) + `"]}}`)
	configDigest := sha256.Sum256(config)
	configName := fmt.Sprintf("%x.json", configDigest)
	manifest := []byte(fmt.Sprintf(`[{"Config":%q,"RepoTags":[],"Layers":["layer/layer.tar"]}]`, configName))
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, member := range []struct {
		name string
		body []byte
	}{{"manifest.json", manifest}, {configName, config}, {"layer/layer.tar", []byte("layer-" + id)}} {
		if err := writer.WriteHeader(&tar.Header{Name: member.name, Mode: 0o400, Size: int64(len(member.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := canonicalTemporaryDirectory(t)
	path := filepath.Join(root, id+".tar")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archive.Bytes())
	return AssetSpec{
		ID: id, Kind: dockerImageKind, Identity: "sha256:" + fmt.Sprintf("%x", configDigest), Ownership: OwnershipChora,
		AcquisitionKind: AcquisitionLocalDockerArchive, ArchivePath: path, ArchiveSize: int64(archive.Len()), ArchiveSHA256: fmt.Sprintf("%x", archiveDigest),
	}, slices.Clone(archive.Bytes())
}

func (*traceDockerRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected Start")
}

func engineObservationHandler(t *testing.T, contextName, endpoint string) func([]string) (dockersupervisor.CommandResult, error) {
	t.Helper()
	return func(args []string) (dockersupervisor.CommandResult, error) {
		switch args[0] {
		case "version":
			return dockersupervisor.CommandResult{Stdout: []byte(`{"api_version":"1.47","server_version":"27.0.0","operating_system":"linux","architecture":"arm64"}`)}, nil
		case "info":
			return dockersupervisor.CommandResult{Stdout: []byte(`{"daemon_id":"daemon-1","provider_name":"Docker Desktop"}`)}, nil
		case "context":
			if slices.Equal(args, []string{"context", "show"}) {
				return dockersupervisor.CommandResult{Stdout: []byte(contextName)}, nil
			}
			encoded, _ := json.Marshal(endpoint)
			return dockersupervisor.CommandResult{Stdout: encoded}, nil
		default:
			t.Fatalf("unexpected observation command %q", args)
			return dockersupervisor.CommandResult{}, nil
		}
	}
}

type fakeClock struct{ now time.Time }

func (clock fakeClock) Now() time.Time { return clock.now }

func canonicalTemporaryDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("mode %s = %v/%v, want %v", path, info, err, mode)
	}
}

func waitError(t *testing.T, channel <-chan error) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for file lock result")
		return nil
	}
}
