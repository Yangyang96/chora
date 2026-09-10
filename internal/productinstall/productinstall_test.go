package productinstall

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var errInjectedCrash = errors.New("injected crash")

type installOutcome struct {
	result InstallResult
	err    error
}

type fakeWorld struct {
	now         time.Time
	target      EngineTarget
	observation EngineObservation
	observerErr error
	state       State
	inventory   map[string]bool
	references  GenerationReferences

	observerCalls   int
	authorizerCalls int
	lockCalls       int
	storeLoads      int
	storeSaves      int
	inspectCalls    int
	acquireCalls    []AssetSpec
	removeCalls     []AssetSpec
	probeCalls      int
	verifyCalls     int
	activateCalls   int
	deactivateCalls int

	denyAuthority              bool
	probeResult                ProbeResult
	probeErr                   error
	verifyErr                  error
	interruptAcquire           bool
	interruptRemove            bool
	interruptProbe             bool
	interruptVerify            bool
	interruptActivate          bool
	failSaveAfterActivation    bool
	failSaveWhenServingRetired bool
	failNextSave               bool
	currentGeneration          string

	installationMu  sync.Mutex
	counterMu       sync.Mutex
	lockAttempts    chan struct{}
	acquireEntered  chan struct{}
	acquireContinue chan struct{}
}

func newFakeWorld() *fakeWorld {
	target := EngineTarget{
		CLIPath:        "/usr/local/bin/docker",
		ContextName:    "chora-local",
		EndpointDigest: digest("e"),
	}
	observation := EngineObservation{
		DaemonID:        "daemon-1",
		APIVersion:      "1.47",
		OperatingSystem: "linux",
		Architecture:    "arm64",
		ContextName:     target.ContextName,
		EndpointDigest:  target.EndpointDigest,
	}
	return &fakeWorld{
		now:         time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		target:      target,
		observation: observation,
		state:       EmptyState(),
		inventory:   map[string]bool{},
		probeResult: ProbeResult{
			Passed:              true,
			CleanupProven:       true,
			QualificationDigest: digest("f"),
			Engine:              observation,
		},
	}
}

func (world *fakeWorld) service(t *testing.T) *Service {
	t.Helper()
	service, err := New(Dependencies{
		Clock: world, Locker: world, Observer: world, Authorizer: world, Store: world,
		Inspector: world, Acquirer: world, Remover: world, Prober: world,
		Verifier: world, Activator: world, References: world,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (world *fakeWorld) Now() time.Time { return world.now }

func (world *fakeWorld) Observe(_ context.Context, target EngineTarget) (EngineObservation, error) {
	world.observerCalls++
	if target != world.target {
		return EngineObservation{}, ErrInvalidRequest
	}
	return world.observation, world.observerErr
}

func (world *fakeWorld) Authorize(_ context.Context, _ MutationAuthority, _ Action) error {
	world.counterMu.Lock()
	defer world.counterMu.Unlock()
	world.authorizerCalls++
	if world.denyAuthority {
		return ErrAuthorityRequired
	}
	return nil
}

func (world *fakeWorld) WithInstallationLock(ctx context.Context, fn func(context.Context) error) error {
	if world.lockAttempts != nil {
		world.lockAttempts <- struct{}{}
	}
	world.installationMu.Lock()
	defer world.installationMu.Unlock()
	world.lockCalls++
	return fn(ctx)
}

func (world *fakeWorld) Load(context.Context) (State, error) {
	world.storeLoads++
	return cloneState(world.state), nil
}

func (world *fakeWorld) SaveCAS(_ context.Context, expected uint64, state State) error {
	world.storeSaves++
	if world.failSaveWhenServingRetired {
		for _, operation := range state.Operations {
			if operation.RetiredServingGenerationID != "" {
				world.failSaveWhenServingRetired = false
				return errInjectedCrash
			}
		}
	}
	if world.failNextSave {
		world.failNextSave = false
		return errInjectedCrash
	}
	if world.state.Revision != expected {
		return ErrConcurrentUpdate
	}
	world.state = cloneState(state)
	return nil
}

func (world *fakeWorld) InspectExact(_ context.Context, target EngineTarget, asset AssetSpec) (AssetStatus, error) {
	world.inspectCalls++
	if target != world.target {
		return AssetStatus{}, ErrInvalidRequest
	}
	if world.inventory[asset.Identity] {
		return AssetStatus{Present: true, Identity: asset.Identity}, nil
	}
	return AssetStatus{}, nil
}

func (world *fakeWorld) AcquirePinned(_ context.Context, target EngineTarget, asset AssetSpec) error {
	if target != world.target {
		return ErrInvalidRequest
	}
	world.acquireCalls = append(world.acquireCalls, asset)
	world.inventory[asset.Identity] = true
	if world.acquireEntered != nil {
		close(world.acquireEntered)
		<-world.acquireContinue
		world.acquireEntered = nil
	}
	if world.interruptAcquire {
		world.interruptAcquire = false
		return ErrInterrupted
	}
	return nil
}

func (world *fakeWorld) RemoveExact(_ context.Context, target EngineTarget, asset AssetSpec) error {
	if target != world.target {
		return ErrInvalidRequest
	}
	world.removeCalls = append(world.removeCalls, asset)
	delete(world.inventory, asset.Identity)
	if world.interruptRemove {
		world.interruptRemove = false
		return ErrInterrupted
	}
	return nil
}

func (world *fakeWorld) Probe(_ context.Context, authority MutationAuthority, target EngineTarget, _ GenerationSpec) (ProbeResult, error) {
	world.probeCalls++
	if target != world.target || authority.GrantID == "" {
		return ProbeResult{}, ErrInvalidRequest
	}
	if world.interruptProbe {
		world.interruptProbe = false
		return ProbeResult{}, ErrInterrupted
	}
	return world.probeResult, world.probeErr
}

func (world *fakeWorld) Verify(_ context.Context, target EngineTarget, _ GenerationSpec, _ []InstalledAsset, _ ProbeResult) error {
	world.verifyCalls++
	if target != world.target {
		return ErrInvalidRequest
	}
	if world.interruptVerify {
		world.interruptVerify = false
		return ErrInterrupted
	}
	return world.verifyErr
}

func (world *fakeWorld) Current(context.Context) (string, error) {
	return world.currentGeneration, nil
}

func (world *fakeWorld) AtomicActivate(_ context.Context, expected string, generation GenerationSpec) error {
	world.activateCalls++
	if world.currentGeneration != expected {
		return ErrConcurrentUpdate
	}
	world.currentGeneration = generation.GenerationID
	if world.failSaveAfterActivation {
		world.failNextSave = true
		world.failSaveAfterActivation = false
	}
	if world.interruptActivate {
		world.interruptActivate = false
		return ErrInterrupted
	}
	return nil
}

func (world *fakeWorld) AtomicDeactivate(_ context.Context, expected string) error {
	world.deactivateCalls++
	if world.currentGeneration != expected {
		return ErrConcurrentUpdate
	}
	world.currentGeneration = ""
	return nil
}

func (world *fakeWorld) References(context.Context) (GenerationReferences, error) {
	return GenerationReferences{
		ServingGenerationIDs:            slices.Clone(world.references.ServingGenerationIDs),
		ActiveAttemptGenerationIDs:      slices.Clone(world.references.ActiveAttemptGenerationIDs),
		RecoverableAttemptGenerationIDs: slices.Clone(world.references.RecoverableAttemptGenerationIDs),
	}, nil
}

func (world *fakeWorld) RequireReferenceSnapshot(context.Context) error { return nil }

func (world *fakeWorld) RetireServingGeneration(_ context.Context, generationID string) error {
	index, found := slices.BinarySearch(world.references.ServingGenerationIDs, generationID)
	if found {
		world.references.ServingGenerationIDs = slices.Delete(world.references.ServingGenerationIDs, index, index+1)
	}
	return nil
}

func TestDoctorIsReadOnlyAndSetupRequiresAuthorityBeforeMutation(t *testing.T) {
	world := newFakeWorld()
	service := world.service(t)
	report, err := service.Doctor(context.Background(), world.target)
	if err != nil || !report.Ready || report.Engine != world.observation {
		t.Fatalf("Doctor() = (%+v, %v)", report, err)
	}
	if world.observerCalls != 1 || world.lockCalls != 0 || world.storeLoads != 0 || world.storeSaves != 0 || len(world.acquireCalls) != 0 || world.activateCalls != 0 {
		t.Fatalf("Doctor mutated state: %+v", world)
	}

	request := InstallRequest{Key: "setup-1", Target: world.target, Generation: generationSpec("g1", ownedAsset("agent", "a"))}
	_, err = service.Setup(context.Background(), request)
	if !errors.Is(err, ErrAuthorityRequired) {
		t.Fatalf("Setup() error = %v", err)
	}
	if world.authorizerCalls != 0 || world.lockCalls != 0 || world.storeLoads != 0 || world.storeSaves != 0 || len(world.acquireCalls) != 0 || world.activateCalls != 0 {
		t.Fatalf("authority-free request mutated state: %+v", world)
	}

	request.Authority = authority(ActionSetup)
	world.denyAuthority = true
	_, err = service.Setup(context.Background(), request)
	if !errors.Is(err, ErrAuthorityRequired) {
		t.Fatalf("denied Setup() error = %v", err)
	}
	if world.lockCalls != 0 || world.storeLoads != 0 || world.storeSaves != 0 || len(world.acquireCalls) != 0 || world.activateCalls != 0 {
		t.Fatalf("denied request mutated state: %+v", world)
	}
}

func TestDoctorNeverDisclosesObserverErrors(t *testing.T) {
	world := newFakeWorld()
	world.observerErr = errors.New("Bearer top-secret\n/private/daemon.sock: permission denied")
	report, err := world.service(t).Doctor(context.Background(), world.target)
	if err != nil || report.Ready || report.Reason != DoctorReasonObservationUnavailable {
		t.Fatalf("Doctor() = (%+v, %v)", report, err)
	}
	for _, secret := range []string{"top-secret", "/private/daemon.sock", "permission denied", "\n"} {
		if strings.Contains(report.Reason, secret) {
			t.Fatalf("Doctor Reason leaked %q: %q", secret, report.Reason)
		}
	}
	if world.lockCalls != 0 || world.storeLoads != 0 || world.storeSaves != 0 {
		t.Fatalf("Doctor acquired mutation resources: %+v", world)
	}
}

func TestAcquisitionKindsRejectBuildLoadAndPrune(t *testing.T) {
	for _, kind := range []AcquisitionKind{"web_build", "docker_build", "image_load", "engine_prune", "build", "load", "prune"} {
		asset := ownedAsset("agent", "a")
		asset.AcquisitionKind = kind
		if err := validateAsset(asset); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("validateAsset(%q) error = %v", kind, err)
		}
	}
	legacy := ownedAsset("agent", "a")
	legacy.AcquisitionKind = AcquisitionRegistryPull
	legacy.AcquisitionRef = "registry.invalid/chora/agent@" + legacy.Identity
	legacy.ArchivePath, legacy.ArchiveSize, legacy.ArchiveSHA256 = "", 0, ""
	if err := validateAsset(legacy); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("legacy Registry acquisition error = %v", err)
	}
	for _, kind := range []AcquisitionKind{AcquisitionPrivateRuntime, AcquisitionSourceBundle} {
		asset := ownedAsset("agent", "a")
		asset.AcquisitionKind = kind
		asset.AcquisitionRef = "/private/chora/asset"
		asset.ArchivePath, asset.ArchiveSize, asset.ArchiveSHA256 = "", 0, ""
		asset.Kind = "private_asset"
		if err := validateAsset(asset); err != nil {
			t.Fatalf("validateAsset(%q) error = %v", kind, err)
		}
	}
}

func TestExternalInstallationLockSerializesServiceInstances(t *testing.T) {
	world := newFakeWorld()
	world.lockAttempts = make(chan struct{}, 2)
	world.acquireEntered = make(chan struct{})
	world.acquireContinue = make(chan struct{})
	request := InstallRequest{
		Authority: authority(ActionSetup), Key: "setup-concurrent", Target: world.target,
		Generation: generationSpec("g1", ownedAsset("agent", "a")),
	}
	firstDone := make(chan installOutcome, 1)
	secondDone := make(chan installOutcome, 1)
	firstService := world.service(t)
	secondService := world.service(t)
	go func() {
		result, err := firstService.Setup(context.Background(), request)
		firstDone <- installOutcome{result, err}
	}()
	waitSignal(t, world.lockAttempts, "first lock attempt")
	waitSignal(t, world.acquireEntered, "first acquisition")
	go func() {
		result, err := secondService.Setup(context.Background(), request)
		secondDone <- installOutcome{result, err}
	}()
	waitSignal(t, world.lockAttempts, "second lock attempt")
	select {
	case result := <-secondDone:
		t.Fatalf("second Service escaped external serialization: %+v", result)
	default:
	}
	close(world.acquireContinue)
	first := waitOutcome(t, firstDone)
	second := waitOutcome(t, secondDone)
	if first.err != nil || second.err != nil || first.result.Replayed || !second.result.Replayed {
		t.Fatalf("serialized results = first=%+v second=%+v", first, second)
	}
	if len(world.acquireCalls) != 1 || world.activateCalls != 1 || world.lockCalls != 2 {
		t.Fatalf("duplicate external effect: acquire=%+v activate=%d locks=%d", world.acquireCalls, world.activateCalls, world.lockCalls)
	}
}

func TestSetupReusesExactAssetsAndAcquiresOnlyPinnedMissingAssets(t *testing.T) {
	world := newFakeWorld()
	pi := externalAsset("pi", "d")
	agent := ownedAsset("agent", "a")
	boundary := ownedAsset("boundary", "b")
	world.inventory[pi.Identity] = true
	world.inventory[agent.Identity] = true
	spec := generationSpec("g1", agent, boundary, pi)
	request := InstallRequest{Authority: authority(ActionSetup), Key: "setup-1", Target: world.target, Generation: spec}

	result, err := world.service(t).Setup(context.Background(), request)
	if err != nil || !result.Active || result.GenerationID != "g1" {
		t.Fatalf("Setup() = (%+v, %v)", result, err)
	}
	if len(world.acquireCalls) != 1 || world.acquireCalls[0] != boundary {
		t.Fatalf("AcquirePinned calls = %+v", world.acquireCalls)
	}
	if world.activateCalls != 1 || world.currentGeneration != "g1" || world.state.ActiveGenerationID != "g1" {
		t.Fatalf("activation not committed: current=%q state=%+v", world.currentGeneration, world.state)
	}
	stored, _ := generationByID(&world.state, "g1")
	if stored == nil || !assetReused(*stored, "agent") || !assetReused(*stored, "pi") || assetReused(*stored, "boundary") {
		t.Fatalf("reuse provenance = %+v", stored)
	}
	result, err = world.service(t).Setup(context.Background(), request)
	if err != nil || !result.Replayed || len(world.acquireCalls) != 1 || world.activateCalls != 1 {
		t.Fatalf("idempotent replay = (%+v, %v), acquire=%d activate=%d", result, err, len(world.acquireCalls), world.activateCalls)
	}
}

func TestGenerationIdentityCannotRebindLocalArchive(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	spec := generationSpec("g1", asset)
	world.inventory[asset.Identity] = true
	request := InstallRequest{Authority: authority(ActionSetup), Key: "setup-original", Target: world.target, Generation: spec}
	if _, err := world.service(t).Setup(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	drifted := spec
	drifted.Assets = slices.Clone(spec.Assets)
	drifted.Assets[0].ArchivePath = "/private/chora/releases/substituted.tar"
	drifted.Assets[0].ArchiveSHA256 = digest("f")
	request.Key = "setup-rebound"
	request.Generation = drifted
	if _, err := world.service(t).Setup(context.Background(), request); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("archive rebinding error = %v", err)
	}
	if len(world.acquireCalls) != 0 || world.activateCalls != 1 || len(world.state.Operations) != 1 {
		t.Fatalf("archive rebinding crossed mutation boundary: acquire=%+v activate=%d operations=%+v", world.acquireCalls, world.activateCalls, world.state.Operations)
	}
}

func TestProbeFailureNeverActivatesAndCleansOnlyAcquiredAssets(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	world.probeResult.Passed = false
	world.probeResult.Reason = "forced termination unproven"
	request := InstallRequest{Authority: authority(ActionSetup), Key: "setup-fail", Target: world.target, Generation: generationSpec("g1", asset)}

	_, err := world.service(t).Setup(context.Background(), request)
	if !errors.Is(err, ErrOperationFailed) || world.activateCalls != 0 || world.currentGeneration != "" || world.state.ActiveGenerationID != "" {
		t.Fatalf("failed Probe crossed activation boundary: err=%v world=%+v", err, world)
	}
	if world.inventory[asset.Identity] || len(world.removeCalls) != 1 || world.removeCalls[0] != asset {
		t.Fatalf("acquired asset cleanup = inventory=%v removals=%+v", world.inventory, world.removeCalls)
	}
	if world.state.Operations[0].Phase != PhaseFailed {
		t.Fatalf("journal phase = %s", world.state.Operations[0].Phase)
	}
}

func TestFailedUpgradeRetainsCurrentAndRemovesOnlyReplacementAssets(t *testing.T) {
	world := newFakeWorld()
	shared := ownedAsset("agent", "a")
	oldOnly := ownedAsset("boundary", "b")
	newOnly := ownedAsset("web", "c")
	oldSpec := generationSpec("g1", shared, oldOnly)
	world.state.Generations = []Generation{installedGeneration(oldSpec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	world.inventory[shared.Identity] = true
	world.inventory[oldOnly.Identity] = true
	world.verifyErr = errors.New("replacement verification failed")
	request := InstallRequest{Authority: authority(ActionUpgrade), Key: "upgrade-1", Target: world.target, Generation: generationSpec("g2", shared, newOnly)}

	_, err := world.service(t).Upgrade(context.Background(), request)
	if !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if world.currentGeneration != "g1" || world.state.ActiveGenerationID != "g1" || world.activateCalls != 0 {
		t.Fatalf("failed upgrade replaced current: %+v", world)
	}
	if !world.inventory[shared.Identity] || !world.inventory[oldOnly.Identity] || world.inventory[newOnly.Identity] {
		t.Fatalf("rollback inventory = %v", world.inventory)
	}
	if len(world.removeCalls) != 1 || world.removeCalls[0] != newOnly {
		t.Fatalf("rollback removals = %+v", world.removeCalls)
	}
}

func TestInstallJournalRecoversEachExternallyInterruptibleStage(t *testing.T) {
	tests := []struct {
		name      string
		interrupt func(*fakeWorld)
		phase     OperationPhase
	}{
		{"acquisition", func(world *fakeWorld) { world.interruptAcquire = true }, PhaseAcquiring},
		{"Probe", func(world *fakeWorld) { world.interruptProbe = true }, PhaseProbing},
		{"verification", func(world *fakeWorld) { world.interruptVerify = true }, PhaseVerifying},
		{"activation", func(world *fakeWorld) { world.interruptActivate = true }, PhaseActivating},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			world := newFakeWorld()
			asset := ownedAsset("agent", "a")
			test.interrupt(world)
			request := InstallRequest{Authority: authority(ActionSetup), Key: "recover-1", Target: world.target, Generation: generationSpec("g1", asset)}
			_, err := world.service(t).Setup(context.Background(), request)
			if !errors.Is(err, ErrInterrupted) {
				t.Fatalf("first Setup() error = %v", err)
			}
			if world.state.Operations[0].Phase != test.phase {
				t.Fatalf("journal phase = %s, want %s", world.state.Operations[0].Phase, test.phase)
			}
			result, err := world.service(t).Setup(context.Background(), request)
			if err != nil || !result.Active || !result.Replayed || world.currentGeneration != "g1" || world.state.ActiveGenerationID != "g1" {
				t.Fatalf("resumed Setup() = (%+v, %v), world=%+v", result, err, world)
			}
			if len(world.acquireCalls) != 1 {
				t.Fatalf("acquisition repeated: %+v", world.acquireCalls)
			}
		})
	}
}

func TestActivationCommitRecoversWhenStateCheckpointCrashes(t *testing.T) {
	world := newFakeWorld()
	world.failSaveAfterActivation = true
	asset := ownedAsset("agent", "a")
	request := InstallRequest{Authority: authority(ActionSetup), Key: "recover-activation", Target: world.target, Generation: generationSpec("g1", asset)}
	_, err := world.service(t).Setup(context.Background(), request)
	if !errors.Is(err, errInjectedCrash) || world.currentGeneration != "g1" || world.state.ActiveGenerationID != "" || world.state.Operations[0].Phase != PhaseActivating {
		t.Fatalf("crash boundary = err=%v current=%q state=%+v", err, world.currentGeneration, world.state)
	}
	result, err := world.service(t).Setup(context.Background(), request)
	if err != nil || !result.Active || !result.Replayed || world.activateCalls != 1 || world.state.ActiveGenerationID != "g1" {
		t.Fatalf("activation recovery = (%+v, %v), world=%+v", result, err, world)
	}
}

func TestGarbageCollectHonorsServingGenerationReferences(t *testing.T) {
	world := newFakeWorld()
	activeShared := ownedAsset("agent", "a")
	referencedAsset := ownedAsset("boundary", "b")
	collectableAsset := ownedAsset("web", "c")
	active := generationSpec("g3", activeShared)
	referenced := generationSpec("g1", referencedAsset)
	collectable := generationSpec("g2", activeShared, collectableAsset)
	world.state.Generations = []Generation{
		installedGeneration(referenced, GenerationSuperseded),
		installedGeneration(collectable, GenerationSuperseded),
		installedGeneration(active, GenerationActive),
	}
	world.state.ActiveGenerationID = "g3"
	world.currentGeneration = "g3"
	world.references.ServingGenerationIDs = []string{"g1"}
	for _, asset := range []AssetSpec{activeShared, referencedAsset, collectableAsset} {
		world.inventory[asset.Identity] = true
	}
	unrelatedIdentity := digest("e")
	world.inventory[unrelatedIdentity] = true
	request := GCRequest{Authority: authority(ActionGarbageGC), Key: "gc-1", Target: world.target, Limit: maxGCAssets}

	result, err := world.service(t).GarbageCollect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.SkippedGenerationIDs, []string{"g1"}) || !slices.Equal(result.RemovedAssetIDs, []string{"web"}) {
		t.Fatalf("GC result = %+v", result)
	}
	if !world.inventory[activeShared.Identity] || !world.inventory[referencedAsset.Identity] || world.inventory[collectableAsset.Identity] == true || !world.inventory[unrelatedIdentity] {
		t.Fatalf("GC inventory = %v", world.inventory)
	}
	g1, _ := generationByID(&world.state, "g1")
	g2, _ := generationByID(&world.state, "g2")
	g3, _ := generationByID(&world.state, "g3")
	if g1.Status != GenerationSuperseded || g2.Status != GenerationRemoved || g3.Status != GenerationActive {
		t.Fatalf("GC statuses = %s/%s/%s", g1.Status, g2.Status, g3.Status)
	}
}

func TestUninstallIsAssetLevelAndPreservesExternalAndUnrelatedState(t *testing.T) {
	world := newFakeWorld()
	pi := externalAsset("pi", "d")
	agent := ownedAsset("agent", "a")
	bundle := ownedAsset("source-bundle", "b")
	spec := generationSpec("g1", agent, pi, bundle)
	world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	world.references.ServingGenerationIDs = []string{"g1"}
	for _, asset := range []AssetSpec{pi, agent, bundle} {
		world.inventory[asset.Identity] = true
	}
	unrelated := digest("e")
	world.inventory[unrelated] = true
	request := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-1", Target: world.target}

	result, err := world.service(t).Uninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(result.RemovedAssetIDs)
	if !slices.Equal(result.RemovedAssetIDs, []string{"agent", "source-bundle"}) {
		t.Fatalf("Uninstall result = %+v", result)
	}
	if world.currentGeneration != "" || world.state.ActiveGenerationID != "" || world.deactivateCalls != 1 {
		t.Fatalf("deactivation = current=%q state=%q calls=%d", world.currentGeneration, world.state.ActiveGenerationID, world.deactivateCalls)
	}
	if len(world.references.ServingGenerationIDs) != 0 {
		t.Fatalf("Uninstall left dangling serving references: %+v", world.references)
	}
	if !world.inventory[pi.Identity] || world.inventory[agent.Identity] || world.inventory[bundle.Identity] || !world.inventory[unrelated] {
		t.Fatalf("uninstall inventory = %v", world.inventory)
	}
	for _, removed := range world.removeCalls {
		if removed.Ownership != OwnershipChora {
			t.Fatalf("external PATH Pi removed: %+v", removed)
		}
	}
}

func TestExplicitActiveUninstallRetiresServingReferenceAndReplays(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	spec := generationSpec("g1", asset)
	world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	world.inventory[asset.Identity] = true
	world.references.ServingGenerationIDs = []string{"g1"}
	world.interruptRemove = true
	request := UninstallRequest{
		Authority: authority(ActionUninstall), Key: "uninstall-explicit-active", Target: world.target, GenerationIDs: []string{"g1"},
	}
	result, err := world.service(t).Uninstall(context.Background(), request)
	if !errors.Is(err, ErrInterrupted) || len(result.RemovedAssetIDs) != 0 || result.Replayed {
		t.Fatalf("interrupted explicit active Uninstall = (%+v, %v)", result, err)
	}
	if world.state.ActiveGenerationID != "" || world.currentGeneration != "" || len(world.references.ServingGenerationIDs) != 0 ||
		world.state.Operations[0].RetiredServingGenerationID != "g1" {
		t.Fatalf("explicit active Uninstall transition = state=%q current=%q refs=%+v", world.state.ActiveGenerationID, world.currentGeneration, world.references)
	}
	result, err = world.service(t).Uninstall(context.Background(), request)
	if err != nil || !result.Replayed || !slices.Equal(result.RemovedAssetIDs, []string{"agent"}) {
		t.Fatalf("explicit active Uninstall replay = (%+v, %v)", result, err)
	}
}

func TestServingRetirementProofIsPersistedAfterRetirementAndReplayRepairsCrashGap(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	spec := generationSpec("g1", asset)
	world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	world.inventory[asset.Identity] = true
	world.references.ServingGenerationIDs = []string{"g1"}
	world.failSaveWhenServingRetired = true
	request := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-retirement-proof-crash", Target: world.target}

	_, err := world.service(t).Uninstall(context.Background(), request)
	if !errors.Is(err, errInjectedCrash) {
		t.Fatalf("retirement proof checkpoint error = %v", err)
	}
	if world.state.ActiveGenerationID != "" || world.state.Operations[0].Phase != PhaseRemoving ||
		world.state.Operations[0].RetiredServingGenerationID != "" || len(world.references.ServingGenerationIDs) != 0 {
		t.Fatalf("crash gap state=%+v refs=%+v", world.state, world.references)
	}
	// A publisher that sees no durable proof conservatively restores serving
	// protection. Exact replay must retire it again before checkpointing proof.
	world.references.ServingGenerationIDs = []string{"g1"}
	result, err := world.service(t).Uninstall(context.Background(), request)
	if err != nil || !result.Replayed || world.state.Operations[0].RetiredServingGenerationID != "g1" || len(world.references.ServingGenerationIDs) != 0 {
		t.Fatalf("retirement crash replay = (%+v, %v), state=%+v refs=%+v", result, err, world.state, world.references)
	}
}

func TestRetiredServingGenerationProofValidationIsStrictAndLegacyRowsRemainConservative(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	spec := generationSpec("g1", asset)
	world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
	world.state.ActiveGenerationID = "g1"
	world.currentGeneration = "g1"
	world.inventory[asset.Identity] = true
	world.references.ServingGenerationIDs = []string{"g1"}
	world.interruptRemove = true
	request := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-proof-validation", Target: world.target}
	if _, err := world.service(t).Uninstall(context.Background(), request); !errors.Is(err, ErrInterrupted) {
		t.Fatal(err)
	}
	proof := cloneState(world.state)
	if err := validateState(proof); err != nil {
		t.Fatalf("valid retirement proof rejected: %v", err)
	}
	legacy := cloneState(proof)
	legacy.Operations[0].RetiredServingGenerationID = ""
	if err := validateState(legacy); err != nil {
		t.Fatalf("legacy proofless row should remain readable and conservative: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{"wrong generation", func(state *State) { state.Operations[0].RetiredServingGenerationID = "g2" }},
		{"missing target", func(state *State) { state.Operations[0].TargetGenerationIDs = nil }},
		{"pre-retirement phase", func(state *State) { state.Operations[0].Phase = PhaseDeactivating }},
		{"still active", func(state *State) {
			state.ActiveGenerationID = "g1"
			state.Generations[0].Status = GenerationActive
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneState(proof)
			test.mutate(&invalid)
			if err := validateState(invalid); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("invalid retirement proof error = %v", err)
			}
		})
	}
}

func TestFullUninstallRejectsRemovedGenerationSetupAndAllowsFreshIdentity(t *testing.T) {
	world := newFakeWorld()
	asset := ownedAsset("agent", "a")
	removedSpec := generationSpec("g1", asset)
	setup := InstallRequest{Authority: authority(ActionSetup), Key: "setup-before-uninstall", Target: world.target, Generation: removedSpec}
	if result, err := world.service(t).Setup(context.Background(), setup); err != nil || !result.Active {
		t.Fatalf("initial Setup() = (%+v, %v)", result, err)
	}
	uninstall := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-all", Target: world.target}
	if result, err := world.service(t).Uninstall(context.Background(), uninstall); err != nil || result.Replayed {
		t.Fatalf("full Uninstall() = (%+v, %v)", result, err)
	}
	if result, err := world.service(t).Uninstall(context.Background(), uninstall); err != nil || !result.Replayed {
		t.Fatalf("full Uninstall() replay = (%+v, %v)", result, err)
	}
	if world.currentGeneration != "" || world.state.ActiveGenerationID != "" || world.activateCalls != 1 || world.deactivateCalls != 1 {
		t.Fatalf("post-uninstall activation = current=%q state=%q activate=%d deactivate=%d", world.currentGeneration, world.state.ActiveGenerationID, world.activateCalls, world.deactivateCalls)
	}
	beforeReplay := cloneState(world.state)
	result, err := world.service(t).Setup(context.Background(), setup)
	if err != nil || !result.Replayed || result.Active || result.GenerationID != "g1" {
		t.Fatalf("removed generation's original Setup replay = (%+v, %v)", result, err)
	}
	if world.currentGeneration != "" || world.activateCalls != 1 || world.state.Revision != beforeReplay.Revision || len(world.state.Operations) != len(beforeReplay.Operations) {
		t.Fatalf("original Setup replay reactivated or mutated removed generation: current=%q state=%+v", world.currentGeneration, world.state)
	}

	reinstallRemoved := InstallRequest{Authority: authority(ActionSetup), Key: "setup-removed-generation", Target: world.target, Generation: removedSpec}
	for attempt := 1; attempt <= 2; attempt++ {
		before := cloneState(world.state)
		result, err := world.service(t).Setup(context.Background(), reinstallRemoved)
		if !errors.Is(err, ErrInvalidRequest) || result != (InstallResult{}) {
			t.Fatalf("removed-generation Setup() attempt %d = (%+v, %v)", attempt, result, err)
		}
		if world.currentGeneration != "" || world.state.ActiveGenerationID != "" || world.activateCalls != 1 || world.state.Revision != before.Revision || len(world.state.Operations) != len(before.Operations) {
			t.Fatalf("rejected Setup() attempt %d mutated state: current=%q state=%+v", attempt, world.currentGeneration, world.state)
		}
	}

	freshSpec := generationSpec("g2", asset)
	freshSpec.ManifestSHA256 = removedSpec.ManifestSHA256
	freshSetup := InstallRequest{Authority: authority(ActionSetup), Key: "setup-fresh-generation", Target: world.target, Generation: freshSpec}
	result, err = world.service(t).Setup(context.Background(), freshSetup)
	if err != nil || !result.Active || result.Replayed || result.GenerationID != "g2" {
		t.Fatalf("fresh-identity Setup() = (%+v, %v)", result, err)
	}
	result, err = world.service(t).Setup(context.Background(), freshSetup)
	if err != nil || !result.Active || !result.Replayed || result.GenerationID != "g2" {
		t.Fatalf("fresh-identity Setup() replay = (%+v, %v)", result, err)
	}
	if world.currentGeneration != "g2" || world.state.ActiveGenerationID != "g2" || world.activateCalls != 2 {
		t.Fatalf("fresh/replayed activation = current=%q state=%q calls=%d", world.currentGeneration, world.state.ActiveGenerationID, world.activateCalls)
	}
	removed, _ := generationByID(&world.state, "g1")
	fresh, _ := generationByID(&world.state, "g2")
	if removed == nil || removed.Status != GenerationRemoved || fresh == nil || fresh.Status != GenerationActive {
		t.Fatalf("generation states = removed=%+v fresh=%+v", removed, fresh)
	}
}

func TestUninstallReferenceBarrierAndRemovalCrashRecovery(t *testing.T) {
	t.Run("reference blocks all mutation", func(t *testing.T) {
		world := newFakeWorld()
		asset := ownedAsset("agent", "a")
		spec := generationSpec("g1", asset)
		world.state.Generations = []Generation{installedGeneration(spec, GenerationActive)}
		world.state.ActiveGenerationID = "g1"
		world.currentGeneration = "g1"
		world.inventory[asset.Identity] = true
		world.references.ServingGenerationIDs = []string{"g1"}
		world.references.ActiveAttemptGenerationIDs = []string{"g1"}
		request := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-blocked", Target: world.target, GenerationIDs: []string{"g1"}}
		_, err := world.service(t).Uninstall(context.Background(), request)
		if !errors.Is(err, ErrGenerationReferenced) || world.storeSaves != 0 || world.deactivateCalls != 0 || len(world.removeCalls) != 0 {
			t.Fatalf("reference barrier = err=%v world=%+v", err, world)
		}
		if !slices.Equal(world.references.ServingGenerationIDs, []string{"g1"}) {
			t.Fatalf("Attempt-blocked Uninstall retired serving reference: %+v", world.references)
		}
	})

	t.Run("removal intent resumes after crash", func(t *testing.T) {
		world := newFakeWorld()
		asset := ownedAsset("agent", "a")
		spec := generationSpec("g1", asset)
		world.state.Generations = []Generation{installedGeneration(spec, GenerationSuperseded)}
		world.inventory[asset.Identity] = true
		world.interruptRemove = true
		request := UninstallRequest{Authority: authority(ActionUninstall), Key: "uninstall-recover", Target: world.target, GenerationIDs: []string{"g1"}}
		_, err := world.service(t).Uninstall(context.Background(), request)
		if !errors.Is(err, ErrInterrupted) || world.state.Operations[0].PendingAssetIdentity != asset.Identity {
			t.Fatalf("first Uninstall() = %v, state=%+v", err, world.state)
		}
		result, err := world.service(t).Uninstall(context.Background(), request)
		if err != nil || !slices.Equal(result.RemovedAssetIDs, []string{"agent"}) || len(world.removeCalls) != 1 {
			t.Fatalf("resumed Uninstall() = (%+v, %v), removals=%+v", result, err, world.removeCalls)
		}
	})
}

func authority(action Action) MutationAuthority {
	return MutationAuthority{
		GrantID: "grant-1", ActorID: "user-1", SessionID: "session-1",
		Action: action, IssuedAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
	}
}

func digest(character string) string { return strings.Repeat(character, 64) }

func ownedAsset(id, identityCharacter string) AssetSpec {
	return AssetSpec{
		ID: id, Kind: dockerImageKind, Identity: "sha256:" + digest(identityCharacter), Ownership: OwnershipChora,
		AcquisitionKind: AcquisitionLocalDockerArchive, ArchivePath: "/private/chora/releases/" + id + ".tar",
		ArchiveSize: 1, ArchiveSHA256: digest(identityCharacter),
	}
}

func externalAsset(id, identityCharacter string) AssetSpec {
	return AssetSpec{ID: id, Kind: id, Identity: digest(identityCharacter), Ownership: OwnershipExternal, AcquisitionKind: AcquisitionNone}
}

func generationSpec(id string, assets ...AssetSpec) GenerationSpec {
	slices.SortFunc(assets, func(left, right AssetSpec) int { return strings.Compare(left.ID, right.ID) })
	return GenerationSpec{
		SchemaVersion: GenerationSchema, GenerationID: id, ReleaseID: "release-" + id,
		ManifestSHA256: digest(string(id[len(id)-1])), Assets: assets,
	}
}

func installedGeneration(spec GenerationSpec, status GenerationStatus) Generation {
	assets := make([]InstalledAsset, 0, len(spec.Assets))
	for _, asset := range spec.Assets {
		assets = append(assets, InstalledAsset{Spec: asset})
	}
	return Generation{
		Spec: spec, Status: status, QualificationDigest: digest("f"), Assets: assets,
		ActivatedAt: time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC),
	}
}

func assetReused(generation Generation, assetID string) bool {
	for _, asset := range generation.Assets {
		if asset.Spec.ID == assetID {
			return asset.Reused
		}
	}
	return false
}

func waitSignal(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitOutcome(t *testing.T, channel <-chan installOutcome) installOutcome {
	t.Helper()
	select {
	case result := <-channel:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Setup result")
		return installOutcome{}
	}
}
