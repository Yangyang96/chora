package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
)

func TestProductInstallationControllerDoctorSupportsInstallationOnlyWithoutMutation(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "installation-only")
	executor := &controllerExecutor{doctor: productCommandResult{
		SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: "doctor",
		ContextName: "chora-local", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
	}}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	view, err := controller.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != localweb.ProductInstallationStatusReady || !view.Engine.Ready || !view.Actions.Setup ||
		view.Actions.Upgrade || view.ActiveGenerationID != "" || view.CandidateGenerationID != "g2" {
		t.Fatalf("installation-only view = %+v", view)
	}
	if len(executor.commands) != 1 || executor.commands[0].action != "doctor" {
		t.Fatalf("doctor commands = %+v", executor.commands)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Doctor created installation state: %v", err)
	}
	encoded, _ := json.Marshal(view)
	for _, private := range []string{"/private/", strings.Repeat("e", 64), "daemon"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public view leaked %q: %s", private, encoded)
		}
	}
}

func TestProductInstallationControllerDoctorMapsOnlySafeIdentityReason(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "doctor-reasons")
	executor := &controllerExecutor{doctor: productCommandResult{
		Status: "failed", Action: "doctor", Reason: productinstall.DoctorReasonIdentityMismatch,
	}}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	view, err := controller.Doctor(context.Background())
	if err != nil || view.ReasonCode != localweb.ProductInstallationReasonIdentityMismatch {
		t.Fatalf("identity mismatch view/error = %+v / %v", view, err)
	}
	executor.doctor.Reason = "Bearer secret\n/private/daemon.sock"
	view, err = controller.Doctor(context.Background())
	if err != nil || view.ReasonCode != localweb.ProductInstallationReasonObservationUnavailable {
		t.Fatalf("hostile reason view/error = %+v / %v", view, err)
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{"Bearer", "secret", "/private/daemon.sock"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("Doctor leaked hostile reason %q: %s", secret, encoded)
		}
	}
}

func TestProductInstallationControllerSetupRequiresExactConfirmations(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "authority")
	executor := &controllerExecutor{doctor: productCommandResult{Status: "passed", Action: "doctor"}}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	request := localweb.ProductInstallationMutationRequest{
		ActorID: "local-user", SessionID: "session-1", IdempotencyKey: "setup-1",
		Action: localweb.ProductInstallationActionSetup, ConfirmPinnedEngineTools: true,
	}
	if _, err := controller.Mutate(context.Background(), request); !errors.Is(err, productinstall.ErrAuthorityRequired) {
		t.Fatalf("partial confirmation error = %v", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("unauthorized setup reached executor: %+v", executor.commands)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthorized setup created state: %v", err)
	}

	request.ConfirmColimaVM = true
	executor.onMutation = func(productCommand) error {
		setControllerActiveGeneration(t, root, "g2")
		return nil
	}
	executor.mutation = productCommandResult{Status: "passed", Action: "setup", GenerationID: "g2", Active: true, Replayed: true}
	view, err := controller.Mutate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 2 || executor.commands[0].action != productinstall.ActionSetup || executor.commands[1].action != "doctor" {
		t.Fatalf("authorized setup commands = %+v", executor.commands)
	}
	command := executor.commands[0]
	if command.key == "" || command.authority.GrantID == "" || command.authority.ActorID == request.ActorID ||
		command.authority.SessionID == request.SessionID || command.colima.DownloadAuthorization != productionColimaPolicy().downloadGrant() ||
		command.colima.VMAuthorization != command.colima.expectedVMAuthorization() {
		t.Fatalf("derived setup authority = %+v / colima=%+v", command.authority, command.colima)
	}
	if !view.Replayed || !view.RestartRequired || view.Status != localweb.ProductInstallationStatusComplete {
		t.Fatalf("setup view = %+v", view)
	}
}

func TestProductInstallationControllerMutationPreservesBlockedPostDoctor(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "blocked-after-setup")
	executor := &controllerExecutor{
		mutation: productCommandResult{Status: "passed", Action: "setup", Replayed: true},
		doctor: productCommandResult{
			Status: "failed", Action: "doctor", Reason: productinstall.DoctorReasonIdentityMismatch,
		},
	}
	executor.onMutation = func(productCommand) error {
		setControllerActiveGeneration(t, root, "g2")
		return nil
	}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	view, err := controller.Mutate(context.Background(), localweb.ProductInstallationMutationRequest{
		ActorID: "user", SessionID: "session", IdempotencyKey: "setup-blocked", Action: localweb.ProductInstallationActionSetup,
		ConfirmPinnedEngineTools: true, ConfirmColimaVM: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != localweb.ProductInstallationStatusBlocked || view.ReasonCode != localweb.ProductInstallationReasonIdentityMismatch ||
		!view.Replayed || !view.RestartRequired || view.Engine.Ready {
		t.Fatalf("post-mutation blocked view = %+v", view)
	}
}

func TestProductInstallationControllerMapsLifecycleWithoutRequestMaterials(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "actions")
	executor := &controllerExecutor{
		mutation: productCommandResult{Status: "passed"},
		doctor:   productCommandResult{Status: "passed", Action: "doctor"},
	}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		public localweb.ProductInstallationAction
		want   productinstall.Action
		limit  int
	}{
		{localweb.ProductInstallationActionUpgrade, productinstall.ActionUpgrade, 0},
		{localweb.ProductInstallationActionGC, productinstall.ActionGarbageGC, 7},
		{localweb.ProductInstallationActionUninstall, productinstall.ActionUninstall, 0},
	}
	for index, test := range tests {
		executor.commands = nil
		request := localweb.ProductInstallationMutationRequest{
			ActorID: "user", SessionID: "session", IdempotencyKey: "request-" + string(rune('a'+index)),
			Action: test.public, Limit: test.limit,
		}
		view, err := controller.Mutate(context.Background(), request)
		if err != nil {
			t.Fatalf("%s mutation: %v", test.public, err)
		}
		if len(executor.commands) != 2 || executor.commands[0].action != test.want || executor.commands[0].limit != test.limit {
			t.Fatalf("%s commands = %+v", test.public, executor.commands)
		}
		if executor.commands[0].releaseRoot != controller.candidate.releaseRoot || executor.commands[0].privatePiSource != controller.candidate.privatePiSource {
			t.Fatalf("%s did not use preconfigured materials", test.public)
		}
		if view.RestartRequired {
			t.Fatalf("%s reported restart without a durable generation change", test.public)
		}
	}
}

func TestProductInstallationControllerRestartRequiredDerivesFromLoadedAndDurableGeneration(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "restart-latch")
	setControllerActiveGeneration(t, root, "g1")
	executor := &controllerExecutor{
		mutation: productCommandResult{Status: "passed"},
		doctor:   productCommandResult{Status: "passed", Action: "doctor"},
	}
	executor.onMutation = func(command productCommand) error {
		if command.action == productinstall.ActionUpgrade {
			setControllerActiveGeneration(t, root, "g2")
		}
		return nil
	}
	controller, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	base := localweb.ProductInstallationMutationRequest{ActorID: "user", SessionID: "session"}
	base.IdempotencyKey, base.Action = "upgrade-latch", localweb.ProductInstallationActionUpgrade
	upgraded, err := controller.Mutate(context.Background(), base)
	if err != nil || !upgraded.RestartRequired {
		t.Fatalf("upgrade latch = %+v / %v", upgraded, err)
	}
	base.IdempotencyKey, base.Action, base.Limit = "gc-after-upgrade", localweb.ProductInstallationActionGC, 4
	commandsBefore := len(executor.commands)
	if _, err := controller.Mutate(context.Background(), base); !errors.Is(err, productinstall.ErrInvalidState) {
		t.Fatalf("GC after Upgrade error = %v", err)
	}
	if len(executor.commands) != commandsBefore {
		t.Fatalf("GC after Upgrade reached executor: %+v", executor.commands[commandsBefore:])
	}
	fresh, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "fresh-gc"
	freshGC, err := fresh.Mutate(context.Background(), base)
	if err != nil || freshGC.RestartRequired {
		t.Fatalf("fresh GC incorrectly required restart = %+v / %v", freshGC, err)
	}
}

func TestProductInstallationControllerExactUpgradeReplayAfterRecreationDoesNotRelatchRestart(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "cross-controller-replay")
	setControllerActiveGeneration(t, root, "g1")
	executor := &controllerExecutor{
		mutation: productCommandResult{Status: "passed", Action: "upgrade", GenerationID: "g2", Active: true},
		doctor:   productCommandResult{Status: "passed", Action: "doctor"},
	}
	executor.onMutation = func(productCommand) error {
		setControllerActiveGeneration(t, root, "g2")
		return nil
	}
	first, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	request := localweb.ProductInstallationMutationRequest{
		ActorID: "user", SessionID: "session", IdempotencyKey: "upgrade-replay", Action: localweb.ProductInstallationActionUpgrade,
	}
	view, err := first.Mutate(context.Background(), request)
	if err != nil || !view.RestartRequired {
		t.Fatalf("first Upgrade = %+v / %v", view, err)
	}

	executor.onMutation = nil
	executor.mutation.Replayed = true
	recreated, err := newProductInstallationController(controllerCandidate(root), executor)
	if err != nil {
		t.Fatal(err)
	}
	view, err = recreated.Mutate(context.Background(), request)
	if err != nil || !view.Replayed || view.RestartRequired || view.ActiveGenerationID != "g2" {
		t.Fatalf("recreated exact replay = %+v / %v", view, err)
	}
}

func TestServingReferenceFullUninstallCompositionAndExactReplay(t *testing.T) {
	root := canonicalTempRoot(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "composition-state")
	setControllerActiveGeneration(t, stateRoot, "g1")
	backend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, err := backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.AtomicActivate(context.Background(), "", state.Generations[0].Spec); err != nil {
		t.Fatal(err)
	}
	executor := &fileBackendUninstallExecutor{backend: backend}
	controller, err := newProductInstallationController(controllerCandidate(stateRoot), executor)
	if err != nil {
		t.Fatal(err)
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "web")
	if err := os.Mkdir(webRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	newPublisherServer := func(databaseName string) *localweb.Server {
		server, serverErr := localweb.NewProduct(context.Background(), filepath.Join(root, databaseName), webRoot, log.New(io.Discard, "", 0), localweb.ProductOptions{
			RepoRoot: repositoryRoot, RepositoryRoot: repositoryRoot, PiAuthFile: filepath.Join(root, "missing-auth.json"),
			InstallationStateRoot: stateRoot, GenerationID: "g1", InstallationController: controller,
			PiPreflight: func(context.Context) preflight.Report { return preflight.Report{Status: preflight.StatusFailed} },
		})
		if serverErr != nil {
			t.Fatalf("construct publisher server: %v", serverErr)
		}
		return server
	}
	server := newPublisherServer("composition.db")
	refs, err := backend.References(context.Background())
	if err != nil || !slices.Equal(refs.ServingGenerationIDs, []string{"g1"}) || len(refs.ActiveAttemptGenerationIDs) != 0 || len(refs.RecoverableAttemptGenerationIDs) != 0 {
		t.Fatalf("initial localweb references = %+v / %v", refs, err)
	}

	mutate := func() (localweb.ProductInstallationView, string) {
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/product-installation/uninstall", strings.NewReader(`{"confirmation":"uninstall"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "composition-uninstall")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("Uninstall status=%d body=%s", response.Code, response.Body.String())
		}
		var view localweb.ProductInstallationView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view, response.Body.String()
	}
	first, publicBody := mutate()
	if !first.RestartRequired || first.Replayed || first.ActiveGenerationID != "" || first.Actions != (localweb.ProductInstallationActionsView{}) {
		t.Fatalf("full Uninstall view = %+v", first)
	}
	for _, private := range []string{"/private/", strings.Repeat("a", 64), "grant-", "session-"} {
		if strings.Contains(publicBody, private) {
			t.Fatalf("public Uninstall response leaked %q: %s", private, publicBody)
		}
	}
	refs, err = backend.References(context.Background())
	if err != nil || len(refs.ServingGenerationIDs) != 0 || len(refs.ActiveAttemptGenerationIDs) != 0 || len(refs.RecoverableAttemptGenerationIDs) != 0 {
		t.Fatalf("Uninstall left references = %+v / %v", refs, err)
	}
	replayed, _ := mutate()
	if !replayed.Replayed || !replayed.RestartRequired || replayed.ActiveGenerationID != "" {
		t.Fatalf("exact Uninstall replay view = %+v", replayed)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server = newPublisherServer("composition-restarted.db")
	t.Cleanup(func() { _ = server.Close() })
	refs, err = backend.References(context.Background())
	if err != nil || len(refs.ServingGenerationIDs) != 0 {
		t.Fatalf("localweb resurrected retired serving reference = %+v / %v", refs, err)
	}
}

type fileBackendUninstallExecutor struct {
	backend *productinstall.FileBackend
}

func (executor *fileBackendUninstallExecutor) Execute(ctx context.Context, command productCommand) (productCommandResult, error) {
	if command.action == "doctor" {
		return productCommandResult{
			SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: "doctor",
			APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64", ContextName: "chora-local",
		}, nil
	}
	if executor == nil || executor.backend == nil || command.action != productinstall.ActionUninstall {
		return productCommandResult{}, productinstall.ErrInvalidRequest
	}
	ports := fileBackendUninstallPorts{target: command.target}
	service, err := productinstall.New(productinstall.Dependencies{
		Clock: ports, Locker: executor.backend, Observer: ports,
		Authorizer: productinstall.ExplicitLocalAuthorizer{
			GrantID: command.authority.GrantID, ActorID: command.authority.ActorID, SessionID: command.authority.SessionID,
		},
		Store: executor.backend, Inspector: ports, Acquirer: ports, Remover: ports,
		Prober: ports, Verifier: ports, Activator: executor.backend, References: executor.backend,
	})
	if err != nil {
		return productCommandResult{}, err
	}
	result, err := service.Uninstall(ctx, productinstall.UninstallRequest{
		Authority: command.authority, Key: command.key, Target: command.target, GenerationIDs: command.generationIDs,
	})
	return productCommandResult{
		SchemaVersion: "chora.product-command-result/v1", Status: "passed", Action: string(command.action),
		RemovedAssetIDs: result.RemovedAssetIDs, Replayed: result.Replayed,
	}, err
}

type fileBackendUninstallPorts struct {
	target productinstall.EngineTarget
}

func (ports fileBackendUninstallPorts) Now() time.Time { return time.Unix(2, 0).UTC() }
func (ports fileBackendUninstallPorts) Observe(context.Context, productinstall.EngineTarget) (productinstall.EngineObservation, error) {
	return productinstall.EngineObservation{
		DaemonID: "daemon", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextName: ports.target.ContextName, EndpointDigest: ports.target.EndpointDigest,
	}, nil
}
func (fileBackendUninstallPorts) InspectExact(context.Context, productinstall.EngineTarget, productinstall.AssetSpec) (productinstall.AssetStatus, error) {
	return productinstall.AssetStatus{}, nil
}
func (fileBackendUninstallPorts) AcquirePinned(context.Context, productinstall.EngineTarget, productinstall.AssetSpec) error {
	return nil
}
func (fileBackendUninstallPorts) RemoveExact(context.Context, productinstall.EngineTarget, productinstall.AssetSpec) error {
	return errors.New("composition attempted to remove an external asset")
}
func (ports fileBackendUninstallPorts) Probe(context.Context, productinstall.MutationAuthority, productinstall.EngineTarget, productinstall.GenerationSpec) (productinstall.ProbeResult, error) {
	return productinstall.ProbeResult{Passed: true, CleanupProven: true, QualificationDigest: strings.Repeat("f", 64)}, nil
}
func (fileBackendUninstallPorts) Verify(context.Context, productinstall.EngineTarget, productinstall.GenerationSpec, []productinstall.InstalledAsset, productinstall.ProbeResult) error {
	return nil
}

func setControllerActiveGeneration(t *testing.T, root, generationID string) {
	t.Helper()
	backend, err := productinstall.NewFileBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion == "" {
		state = productinstall.EmptyState()
	}
	for index := range state.Generations {
		if state.Generations[index].Status == productinstall.GenerationActive {
			state.Generations[index].Status = productinstall.GenerationSuperseded
		}
	}
	if generationID != "" {
		found := false
		for index := range state.Generations {
			if state.Generations[index].Spec.GenerationID == generationID {
				state.Generations[index].Status = productinstall.GenerationActive
				found = true
			}
		}
		if !found {
			asset := productinstall.AssetSpec{
				ID: "asset-" + generationID, Kind: "test", Identity: strings.Repeat("a", 64),
				Ownership: productinstall.OwnershipExternal, AcquisitionKind: productinstall.AcquisitionNone,
			}
			state.Generations = append(state.Generations, productinstall.Generation{
				Spec: productinstall.GenerationSpec{
					SchemaVersion: productinstall.GenerationSchema, GenerationID: generationID,
					ReleaseID: "release-" + generationID, ManifestSHA256: strings.Repeat("b", 64), Assets: []productinstall.AssetSpec{asset},
				},
				Status: productinstall.GenerationActive, QualificationDigest: strings.Repeat("c", 64),
				Assets: []productinstall.InstalledAsset{{Spec: asset}}, ActivatedAt: time.Unix(1, 0).UTC(),
			})
		}
	}
	state.ActiveGenerationID = generationID
	expected := state.Revision
	state.Revision++
	if err := backend.SaveCAS(context.Background(), expected, state); err != nil {
		t.Fatal(err)
	}
}

func controllerCandidate(stateRoot string) productCommand {
	toolRoot := "/private/chora-tools"
	return productCommand{
		action: productinstall.ActionSetup,
		target: productinstall.EngineTarget{
			CLIPath: filepath.Join(toolRoot, "docker-"+dockerClientVersion), ContextName: "chora-local", EndpointDigest: strings.Repeat("e", 64),
		},
		stateRoot: stateRoot, generationID: "g2", releaseRoot: "/private/release", releaseManifest: "manifest.json",
		manifestSHA256: strings.Repeat("a", 64), releaseID: "release-2", probeRuntime: "/private/probe",
		privatePiRoot: "/private/pi", privatePiSource: "/private/pi-source", privatePiManifest: "/private/pi-manifest.json",
		privatePiManifestSHA256: strings.Repeat("b", 64),
		colima:                  colimaBootstrapRequest{Enabled: true, ToolRoot: toolRoot, Profile: "chora-local", ColimaSource: "/private/sources/colima", DockerClientSource: "/private/sources/docker"},
	}
}

type controllerExecutor struct {
	commands   []productCommand
	mutation   productCommandResult
	doctor     productCommandResult
	onMutation func(productCommand) error
}

func (executor *controllerExecutor) Execute(_ context.Context, command productCommand) (productCommandResult, error) {
	executor.commands = append(executor.commands, command)
	if command.action == "doctor" {
		return executor.doctor, nil
	}
	if executor.onMutation != nil {
		if err := executor.onMutation(command); err != nil {
			return productCommandResult{}, err
		}
	}
	return executor.mutation, nil
}
