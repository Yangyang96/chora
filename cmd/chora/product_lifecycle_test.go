package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/productinstall"
)

func TestProductUnauthorizedRequestDoesNotOpenExecutorOrState(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "must-not-exist")
	executor := &fakeProductExecutor{}
	var stdout, stderr bytes.Buffer
	exit := runProduct([]string{
		"setup", "--docker-cli", "/usr/local/bin/docker", "--docker-context", "chora-local",
		"--endpoint-digest", strings.Repeat("e", 64), "--state-root", stateRoot,
		"--key", "setup-1", "--grant", "grant-1", "--actor", "actor-1", "--session", "session-1",
		// --authorize setup is deliberately absent.
	}, &stdout, &stderr, executor)
	if exit != 2 || executor.calls != 0 || stdout.Len() != 0 || stderr.String() != productUsage+"\n" {
		t.Fatalf("unauthorized result = exit=%d calls=%d stdout=%q stderr=%q", exit, executor.calls, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthorized command created state: %v", err)
	}
}

func TestProductLedgerCloseFailureIsJoinedWithoutMaskingPrimaryError(t *testing.T) {
	primary := errors.New("primary setup failure")
	closeFailure := errors.New("ledger close failure")
	closer := &failingProductLedgerCloser{err: closeFailure}
	joined := joinProductLedgerClose(primary, closer)
	if !errors.Is(joined, primary) || !errors.Is(joined, closeFailure) || closer.calls != 1 {
		t.Fatalf("joined close error = %v, calls=%d", joined, closer.calls)
	}
	closer = &failingProductLedgerCloser{err: closeFailure}
	if joined = joinProductLedgerClose(nil, closer); !errors.Is(joined, closeFailure) || closer.calls != 1 {
		t.Fatalf("close-only error = %v, calls=%d", joined, closer.calls)
	}
}

type failingProductLedgerCloser struct {
	err   error
	calls int
}

func (closer *failingProductLedgerCloser) Close() error {
	closer.calls++
	return closer.err
}

func TestProductDoctorJSONIsStableAndPathFree(t *testing.T) {
	executor := &fakeProductExecutor{result: productCommandResult{
		SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: "doctor",
		ContextName: "chora-local", EndpointDigest: strings.Repeat("e", 64), DaemonID: "daemon-1",
		APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
	}}
	var stdout, stderr bytes.Buffer
	exit := runProduct([]string{
		"doctor", "--docker-cli", "/private/tools/docker", "--docker-context", "chora-local",
		"--endpoint-digest", strings.Repeat("e", 64), "--json",
	}, &stdout, &stderr, executor)
	if exit != 0 || stderr.Len() != 0 || executor.calls != 1 {
		t.Fatalf("doctor = exit=%d stderr=%q calls=%d", exit, stderr.String(), executor.calls)
	}
	var result productCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "passed" {
		t.Fatalf("doctor JSON = %q / %+v / %v", stdout.String(), result, err)
	}
	if strings.Contains(stdout.String(), "/private/tools/docker") {
		t.Fatalf("doctor leaked CLI path: %s", stdout.String())
	}
}

func TestProductReplayConflictAndHostileErrorsUseStableCodes(t *testing.T) {
	base := []string{
		"gc", "--docker-cli", "/usr/local/bin/docker", "--docker-context", "chora-local",
		"--endpoint-digest", strings.Repeat("e", 64), "--state-root", "/private/chora-state",
		"--key", "gc-1", "--grant", "grant-1", "--actor", "actor-1", "--session", "session-1",
		"--authorize", "garbage_collect", "--limit", "4", "--json",
		"--private-pi-root", "/private/pi", "--private-pi-source", "/private/pi-source",
		"--private-pi-manifest", "/private/pi-manifest.json", "--private-pi-manifest-sha256", strings.Repeat("a", 64),
	}
	t.Run("replay", func(t *testing.T) {
		executor := &fakeProductExecutor{result: productCommandResult{
			SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: "garbage_collect", Replayed: true,
		}}
		var stdout, stderr bytes.Buffer
		if exit := runProduct(base, &stdout, &stderr, executor); exit != 0 || !strings.Contains(stdout.String(), `"replayed":true`) {
			t.Fatalf("replay = exit=%d output=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
	})
	t.Run("conflict", func(t *testing.T) {
		executor := &fakeProductExecutor{err: productinstall.ErrIdempotencyConflict}
		var stdout, stderr bytes.Buffer
		if exit := runProduct(base, &stdout, &stderr, executor); exit != 1 || !strings.Contains(stdout.String(), `"reason":"idempotency_conflict"`) || stderr.Len() != 0 {
			t.Fatalf("conflict = exit=%d output=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
	})
	t.Run("hostile error", func(t *testing.T) {
		executor := &fakeProductExecutor{err: errors.New("Bearer secret\n/private/daemon.sock: denied")}
		var stdout, stderr bytes.Buffer
		if exit := runProduct(base, &stdout, &stderr, executor); exit != 1 || !strings.Contains(stdout.String(), `"reason":"product_lifecycle_unavailable"`) {
			t.Fatalf("hostile = exit=%d output=%q", exit, stdout.String())
		}
		for _, secret := range []string{"Bearer", "secret", "/private/daemon.sock", "denied"} {
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatalf("hostile error leaked %q: %q/%q", secret, stdout.String(), stderr.String())
			}
		}
	})
}

func TestProductMutationParserRequiresExactActionAuthorization(t *testing.T) {
	for _, authorized := range []string{"gc", "setup", "uninstall", "garbage_collect\n"} {
		executor := &fakeProductExecutor{}
		var stdout, stderr bytes.Buffer
		exit := runProduct([]string{
			"gc", "--docker-cli", "/usr/local/bin/docker", "--docker-context", "chora-local",
			"--endpoint-digest", strings.Repeat("e", 64), "--state-root", "/private/chora-state",
			"--key", "gc-1", "--grant", "grant-1", "--actor", "actor-1", "--session", "session-1",
			"--authorize", authorized, "--limit", "4",
		}, &stdout, &stderr, executor)
		if exit != 2 || executor.calls != 0 {
			t.Fatalf("authorization %q = exit=%d calls=%d", authorized, exit, executor.calls)
		}
	}
}

func TestTopLevelSetupUsesProductStateMachine(t *testing.T) {
	executor := &fakeProductExecutor{result: productCommandResult{
		SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: "setup", GenerationID: "g1", Active: true, Replayed: true,
	}}
	args := []string{
		"setup", "--docker-cli", "/usr/local/bin/docker", "--docker-context", "chora-local",
		"--endpoint-digest", strings.Repeat("e", 64), "--state-root", "/private/chora-state",
		"--key", "setup-1", "--grant", "grant-1", "--actor", "actor-1", "--session", "session-1", "--authorize", "setup",
		"--release-root", "/private/release", "--release-manifest", "manifest.json", "--manifest-sha256", strings.Repeat("a", 64),
		"--release-id", "release-1", "--generation", "g1", "--probe-runtime-root", "/private/probe",
		"--private-pi-root", "/private/pi", "--private-pi-source", "/private/pi-source",
		"--private-pi-manifest", "/private/pi-manifest.json", "--private-pi-manifest-sha256", strings.Repeat("b", 64),
	}
	var stdout, stderr bytes.Buffer
	if exit := runWithProductExecutor(args, &stdout, &stderr, executor); exit != 0 || executor.calls != 1 || executor.last.action != productinstall.ActionSetup || !strings.Contains(stdout.String(), "replayed=true") {
		t.Fatalf("top-level setup = exit=%d calls=%d action=%q stdout=%q stderr=%q", exit, executor.calls, executor.last.action, stdout.String(), stderr.String())
	}
}

func TestPinnedPrivateManifestChecksumMismatchIsReadOnly(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "pi-manifest.json")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "must-not-exist")
	if _, _, err := readPinnedPrivateManifest(manifest, strings.Repeat("a", 64)); !errors.Is(err, productinstall.ErrInvalidRequest) {
		t.Fatalf("checksum mismatch error = %v", err)
	}
	if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checksum mismatch mutated unrelated state: %v", err)
	}
}

func TestPersistedEngineDriftFailsBeforeLifecycleMutation(t *testing.T) {
	endpoint := "unix:///private/run/docker.sock"
	endpointDigest, _ := dockersupervisor.DigestContextEndpoint(endpoint)
	target := productinstall.EngineTarget{CLIPath: "/private/tools/docker", ContextName: "chora-local", EndpointDigest: endpointDigest}
	active := installedRuntimeGeneration(t, target)
	root := filepath.Join(canonicalTempRoot(t), "engine-state")
	backend, _ := productinstall.NewFileBackend(root)
	state := productinstall.EmptyState()
	state.Revision = 1
	state.ActiveGenerationID = "g1"
	state.Generations = []productinstall.Generation{active}
	if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
		t.Fatal(err)
	}
	runner := &productObservationRunner{endpoint: endpoint, daemonID: "daemon-1"}
	observer, err := productinstall.NewExactDockerAssets(runner, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := requirePersistedEngineMatch(context.Background(), backend, target, observer); err != nil {
		t.Fatalf("matching Engine rejected: %v", err)
	}
	runner.daemonID = "daemon-2"
	if err := requirePersistedEngineMatch(context.Background(), backend, target, observer); err == nil {
		t.Fatal("daemon drift passed lifecycle precondition")
	}
}

func TestProductionSetupRejectsMissingPublishedEvidenceBeforeDockerOrState(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := (productionProductExecutor{}).Execute(context.Background(), productCommand{
		action: productinstall.ActionSetup,
		target: productinstall.EngineTarget{
			CLIPath: "/definitely/missing/docker", ContextName: "chora-local", EndpointDigest: strings.Repeat("e", 64),
		},
		stateRoot: stateRoot,
		authority: productinstall.MutationAuthority{
			GrantID: "grant-1", ActorID: "actor-1", SessionID: "session-1", Action: productinstall.ActionSetup,
		},
		releaseRoot: "/definitely/missing/release", releaseManifest: "manifest.json",
		manifestSHA256: strings.Repeat("a", 64), releaseID: "release-1", generationID: "g1",
		probeRuntime: "/definitely/missing/probe",
	})
	if !errors.Is(err, productinstall.ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, statErr := os.Lstat(stateRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing evidence created state: %v", statErr)
	}
}

func TestProductionFullUninstallResumesAfterDeactivation(t *testing.T) {
	fixture := newProductionUninstallFixture(t)
	if err := os.WriteFile(fixture.imageMarker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := fixture.generation
	generation.Status = productinstall.GenerationSuperseded
	writeProductionUninstallReplayState(t, fixture.backend, fixture.command, generation, productinstall.PhaseRemoving)

	result, err := (productionProductExecutor{}).Execute(context.Background(), fixture.command)
	if err != nil || result.Status != "passed" || !result.Replayed || !slicesEqual(result.RemovedAssetIDs, []string{"agent"}) {
		t.Fatalf("resumed production uninstall = (%+v, %v)", result, err)
	}
	if _, err := os.Lstat(fixture.imageMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journaled product asset remains: %v", err)
	}
	state, err := fixture.backend.Load(context.Background())
	if err != nil || state.ActiveGenerationID != "" || state.Generations[0].Status != productinstall.GenerationRemoved || state.Operations[0].Phase != productinstall.PhaseComplete {
		t.Fatalf("resumed state = %+v / %v", state, err)
	}
}

func TestProductionExplicitActiveUninstallResumesAfterDeactivation(t *testing.T) {
	fixture := newProductionUninstallFixture(t)
	fixture.command.generationIDs = []string{"g1"}
	if err := os.WriteFile(fixture.imageMarker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := fixture.generation
	generation.Status = productinstall.GenerationSuperseded
	writeProductionUninstallReplayState(t, fixture.backend, fixture.command, generation, productinstall.PhaseRemoving)

	result, err := (productionProductExecutor{}).Execute(context.Background(), fixture.command)
	if err != nil || result.Status != "passed" || !result.Replayed || !slicesEqual(result.RemovedAssetIDs, []string{"agent"}) {
		t.Fatalf("resumed explicit production uninstall = (%+v, %v)", result, err)
	}
	if _, err := os.Lstat(fixture.imageMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicitly selected product asset remains: %v", err)
	}
	state, err := fixture.backend.Load(context.Background())
	if err != nil || state.Generations[0].Status != productinstall.GenerationRemoved || state.Operations[0].Phase != productinstall.PhaseComplete {
		t.Fatalf("resumed explicit state = %+v / %v", state, err)
	}
}

func TestProductionExplicitUninstallReplayRejectsDifferentSubset(t *testing.T) {
	fixture := newProductionUninstallFixture(t)
	fixture.command.generationIDs = []string{"g1"}
	if err := os.WriteFile(fixture.imageMarker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := fixture.generation
	generation.Status = productinstall.GenerationSuperseded
	writeProductionUninstallReplayState(t, fixture.backend, fixture.command, generation, productinstall.PhaseRemoving)
	state, err := fixture.backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	canonicalReplay := fixture.command
	canonicalReplay.generationIDs = []string{"g1", "g1"}
	if evidence, err := uninstallReplayEngineEvidence(state, canonicalReplay); err != nil || evidence.Spec.GenerationID != "g1" {
		t.Fatalf("canonical explicit replay evidence = generation %q / %v", evidence.Spec.GenerationID, err)
	}

	replay := fixture.command
	replay.generationIDs = []string{"g2"}
	if _, err := (productionProductExecutor{}).Execute(context.Background(), replay); err == nil || !strings.Contains(err.Error(), "journaled installed Engine evidence") {
		t.Fatalf("different explicit subset error = %v", err)
	}
	if _, err := os.Lstat(fixture.imageMarker); err != nil {
		t.Fatalf("different subset mutated product asset: %v", err)
	}
	state, err = fixture.backend.Load(context.Background())
	if err != nil || state.Operations[0].Phase != productinstall.PhaseRemoving || state.Generations[0].Status != productinstall.GenerationSuperseded {
		t.Fatalf("different subset mutated journal = %+v / %v", state, err)
	}
}

func TestProductionFullUninstallResumesInterruptedPrivatePiCleanup(t *testing.T) {
	fixture := newProductionUninstallFixture(t)
	resolver, err := pidistribution.NewResolver(pidistribution.Config{PrivateRoot: fixture.command.privatePiRoot, PrivateAsset: fixture.privatePiAsset})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.InstallPrivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	interrupted, err := pidistribution.NewResolver(pidistribution.Config{
		PrivateRoot: fixture.command.privatePiRoot, PrivateAsset: fixture.privatePiAsset,
		RemoveTree: func(string) error { return errors.New("injected private Pi cleanup interruption") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := interrupted.RemovePrivate(context.Background()); !errors.Is(err, pidistribution.ErrRemovalInterrupted) {
		t.Fatalf("stage interrupted cleanup = %v", err)
	}
	if pending, err := interrupted.RemovalPending(context.Background()); err != nil || !pending {
		t.Fatalf("staged cleanup pending = %t / %v", pending, err)
	}

	generation := fixture.generation
	generation.Status = productinstall.GenerationRemoved
	for index := range generation.Assets {
		generation.Assets[index].Removed = true
	}
	writeProductionUninstallReplayState(t, fixture.backend, fixture.command, generation, productinstall.PhaseComplete)

	result, err := (productionProductExecutor{}).Execute(context.Background(), fixture.command)
	if err != nil || result.Status != "passed" || !result.Replayed || !slicesEqual(result.RemovedAssetIDs, []string{"agent", "private_pi_fallback"}) {
		t.Fatalf("resumed private Pi cleanup = (%+v, %v)", result, err)
	}
	if pending, err := resolver.RemovalPending(context.Background()); err != nil || pending {
		t.Fatalf("post-resume cleanup pending = %t / %v", pending, err)
	}
	if _, present, err := resolver.InspectPrivate(context.Background()); err != nil || present {
		t.Fatalf("post-resume private Pi = present=%t err=%v", present, err)
	}
}

func TestUninstallReplayRejectsNonJournaledTargetAndSubset(t *testing.T) {
	fixture := newProductionUninstallFixture(t)
	generation := fixture.generation
	generation.Status = productinstall.GenerationSuperseded
	writeProductionUninstallReplayState(t, fixture.backend, fixture.command, generation, productinstall.PhaseRemoving)
	runner := &productObservationRunner{endpoint: "unix:///private/run/docker.sock", daemonID: "changed-daemon"}
	observer, err := productinstall.NewExactDockerAssets(runner, fixture.command.target)
	if err != nil {
		t.Fatal(err)
	}
	if err := requirePersistedEngineMatchForCommand(context.Background(), fixture.backend, fixture.command, observer); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("journal replay accepted changed Engine identity: %v", err)
	}
	command := fixture.command
	command.target.ContextName = "other-context"
	if _, err := (productionProductExecutor{}).Execute(context.Background(), command); err == nil || !strings.Contains(err.Error(), "journaled installed Engine evidence") {
		t.Fatalf("non-journaled target error = %v", err)
	}
	if err := os.WriteFile(fixture.imageMarker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = fixture.command
	command.generationIDs = []string{"g1"}
	if _, err := (productionProductExecutor{}).Execute(context.Background(), command); err == nil || !strings.Contains(err.Error(), "journaled installed Engine evidence") {
		t.Fatalf("non-journaled subset error = %v", err)
	}
	if _, err := os.Lstat(fixture.imageMarker); err != nil {
		t.Fatalf("subset conflict mutated product asset: %v", err)
	}
}

type productionUninstallFixture struct {
	command        productCommand
	backend        *productinstall.FileBackend
	generation     productinstall.Generation
	imageMarker    string
	privatePiAsset *pidistribution.PrivateAsset
}

func newProductionUninstallFixture(t *testing.T) productionUninstallFixture {
	t.Helper()
	root := canonicalTempRoot(t)
	endpoint := "unix:///private/run/docker.sock"
	endpointDigest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	imageIdentity := "sha256:" + strings.Repeat("d", 64)
	imageMarker := filepath.Join(root, "image-present")
	dockerCLI := filepath.Join(root, "docker")
	writeProductLifecycleDockerFixture(t, dockerCLI, imageMarker, endpoint, imageIdentity)
	dockerConfig := filepath.Join(root, "docker-config")
	writeExactDockerContextFixture(t, dockerConfig, "chora-local", endpoint)
	writeExactDockerContextFixture(t, dockerConfig, "other-context", endpoint)
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	target := productinstall.EngineTarget{CLIPath: dockerCLI, ContextName: "chora-local", EndpointDigest: endpointDigest}
	generation := installedRuntimeGeneration(t, target)
	asset := productinstall.AssetSpec{
		ID: "agent", Kind: "oci_image", Identity: imageIdentity, Ownership: productinstall.OwnershipChora,
		AcquisitionKind: productinstall.AcquisitionLocalDockerArchive, ArchivePath: filepath.Join(root, "agent.tar"),
		ArchiveSize: 1, ArchiveSHA256: strings.Repeat("a", 64),
	}
	generation.Spec.Assets = []productinstall.AssetSpec{asset}
	generation.Assets = []productinstall.InstalledAsset{{Spec: asset}}

	privateAsset, manifestPath, manifestDigest := writeProductLifecyclePrivatePiFixture(t, root)
	stateRoot := filepath.Join(root, "state")
	backend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	command := productCommand{
		action: productinstall.ActionUninstall, target: target, stateRoot: stateRoot, key: "uninstall-resume",
		authority: productinstall.MutationAuthority{
			GrantID: "grant-1", ActorID: "actor-1", SessionID: "session-1", Action: productinstall.ActionUninstall,
			IssuedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		},
		privatePiRoot: filepath.Join(root, "private-pi"), privatePiSource: privateAsset.SourceRoot,
		privatePiManifest: manifestPath, privatePiManifestSHA256: manifestDigest,
	}
	return productionUninstallFixture{command: command, backend: backend, generation: generation, imageMarker: imageMarker, privatePiAsset: privateAsset}
}

func writeProductionUninstallReplayState(t *testing.T, backend *productinstall.FileBackend, command productCommand, generation productinstall.Generation, phase productinstall.OperationPhase) {
	t.Helper()
	requestedIDs := append([]string(nil), command.generationIDs...)
	slices.Sort(requestedIDs)
	requestedIDs = slices.Compact(requestedIDs)
	digest, err := uninstallRequestDigest(command.target, requestedIDs)
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := requestedIDs
	if len(targetIDs) == 0 {
		targetIDs = []string{generation.Spec.GenerationID}
	}
	operation := productinstall.Operation{
		Key: command.key, RequestDigest: digest, Action: productinstall.ActionUninstall, Authority: command.authority,
		Phase: phase, Target: command.target, PriorActiveGeneration: generation.Spec.GenerationID,
		TargetGenerationIDs: targetIDs, CreatedAt: command.authority.IssuedAt, UpdatedAt: command.authority.IssuedAt,
	}
	if phase == productinstall.PhaseComplete {
		operation.RemovedAssetIDs = []string{generation.Spec.Assets[0].ID}
		operation.RemovedAssetIdentities = []string{generation.Spec.Assets[0].Identity}
	}
	state := productinstall.EmptyState()
	state.Revision = 1
	state.Generations = []productinstall.Generation{generation}
	state.Operations = []productinstall.Operation{operation}
	if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
		t.Fatal(err)
	}
	if err := backend.PublishReferences(context.Background(), productinstall.GenerationReferences{}); err != nil {
		t.Fatal(err)
	}
}

func writeProductLifecycleDockerFixture(t *testing.T, path, imageMarker, endpoint, imageIdentity string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
case "$3:$4" in
  version:*) printf '{"api_version":"1.47","server_version":"29.6.1","operating_system":"linux","architecture":"arm64"}\n' ;;
  info:*) printf '{"daemon_id":"daemon-1","provider_name":"Colima"}\n' ;;
  context:show) printf 'chora-local\n' ;;
  context:inspect) printf '"%s"\n' ;;
  image:inspect)
    if [ -f %q ]; then printf '{"Id":"%s","Os":"linux","Architecture":"arm64"}\n'; else printf 'No such image\n' >&2; exit 1; fi ;;
  image:rm) rm %q ;;
  *) exit 64 ;;
esac
`, endpoint, imageMarker, imageIdentity, imageMarker)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeProductLifecyclePrivatePiFixture(t *testing.T, root string) (*pidistribution.PrivateAsset, string, string) {
	t.Helper()
	source := filepath.Join(root, "pi-source")
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	packageJSON := []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2","private":true}`)
	executable := []byte("#!/bin/sh\nprintf '0.84.2\\n'\n")
	if err := os.WriteFile(filepath.Join(source, "bin", "pi"), executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "package.json"), packageJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	executableDigest := sha256.Sum256(executable)
	packageDigest := sha256.Sum256(packageJSON)
	files := []pidistribution.ManifestFile{
		{Path: "bin/pi", Mode: 0o700, Size: int64(len(executable)), SHA256: fmt.Sprintf("%x", executableDigest[:])},
		{Path: "package.json", Mode: 0o600, Size: int64(len(packageJSON)), SHA256: fmt.Sprintf("%x", packageDigest[:])},
	}
	closure := pidistribution.ClosureDigest(files)
	manifest, err := json.Marshal(pidistribution.Manifest{
		SchemaVersion: pidistribution.ManifestSchema,
		Platform:      pidistribution.ManifestPlatform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
		PackageName:   pidistribution.ExpectedPackageName, Version: pidistribution.SupportedVersion,
		Executable: "bin/pi", Files: files, ClosureSHA256: fmt.Sprintf("%x", closure[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifest)
	manifestPath := filepath.Join(root, "pi-manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	asset := &pidistribution.PrivateAsset{ManifestBytes: manifest, ExpectedManifestSHA256: manifestDigest, SourceRoot: source}
	return asset, manifestPath, fmt.Sprintf("%x", manifestDigest[:])
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type fakeProductExecutor struct {
	calls  int
	result productCommandResult
	err    error
	last   productCommand
}

type productObservationRunner struct {
	endpoint string
	daemonID string
}

func (runner *productObservationRunner) Run(_ context.Context, command dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	switch command.Args[0] {
	case "version":
		return dockersupervisor.CommandResult{Stdout: []byte(`{"api_version":"1.47","server_version":"29.6.1","operating_system":"linux","architecture":"arm64"}`)}, nil
	case "info":
		return dockersupervisor.CommandResult{Stdout: []byte(`{"daemon_id":"` + runner.daemonID + `","provider_name":"Colima"}`)}, nil
	case "context":
		if len(command.Args) == 2 && command.Args[1] == "show" {
			return dockersupervisor.CommandResult{Stdout: []byte("chora-local\n")}, nil
		}
		encoded, _ := json.Marshal(runner.endpoint)
		return dockersupervisor.CommandResult{Stdout: encoded}, nil
	default:
		return dockersupervisor.CommandResult{ExitCode: 1}, errors.New("unexpected Docker command")
	}
}

func (*productObservationRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected Docker Start")
}

func (executor *fakeProductExecutor) Execute(_ context.Context, command productCommand) (productCommandResult, error) {
	executor.calls++
	executor.last = command
	return executor.result, executor.err
}
