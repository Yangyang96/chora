package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/productinstall"
	storecontract "github.com/Yangyang96/chora/internal/store"
	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestSourceCheckoutPiDockerPrepareAndStartBypassInstalledGenerationAuthority(t *testing.T) {
	server, databasePath := newAgentExecutionProductServer(t)
	server.sourceCheckout = true
	server.installationGenerationID = ""
	server.generationPublisher = nil
	server.generationConfigErr = errors.New("installed generation authority must not be consulted")

	task, revisionText, _ := createAcceptedManagedProfileTask(t, server.Handler())
	taskID, err := domain.ParseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		t.Fatal(err)
	}
	meta := func(key string) app.CommandMeta {
		return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	created, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
		CommandMeta: meta("source-checkout-generation-create"), TaskID: taskID, RevisionID: revisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.prepareAndBindManagedAttempt(context.Background(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareRun(prepareCtx, app.PrepareRunRequest{
			CommandMeta: meta("source-checkout-generation-prepare"), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
		})
	})
	if err != nil {
		t.Fatalf("SourceCheckout Pi Docker preparation consulted installed generation authority: %v", err)
	}
	if prepared.Attempt.AgentExecutionProfileBinding().ExecutionProvider() != domain.DockerExecutionProvider {
		t.Fatalf("SourceCheckout Attempt provider = %q, want Docker", prepared.Attempt.AgentExecutionProfileBinding().ExecutionProvider())
	}
	if generationID, found, err := managedGenerationForAttempt(context.Background(), server.store.Reader(), created.Run.ID(), prepared.Attempt.ID()); err != nil || found || generationID != "" {
		t.Fatalf("SourceCheckout persisted installed generation binding=%q found=%v err=%v", generationID, found, err)
	}
	started, err := server.startManagedAttempt(context.Background(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: meta("source-checkout-generation-start"), RunID: created.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil {
		t.Fatalf("SourceCheckout Pi Docker start=%#v err=%v", started, err)
	}
	assertTableCounts(t, databasePath, map[string]int{"runs": 1, "attempts": 1, "runtime_sessions": 1})
}

func TestInstalledProductPiDockerPrepareAndStartStillRequireGenerationAuthority(t *testing.T) {
	server, databasePath := newAgentExecutionProductServer(t)
	generationErr := errors.New("installed generation authority unavailable")
	server.installationGenerationID = ""
	server.generationPublisher = nil
	server.generationConfigErr = generationErr

	task, revisionText, _ := createAcceptedManagedProfileTask(t, server.Handler())
	taskID, err := domain.ParseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		t.Fatal(err)
	}
	meta := func(key string) app.CommandMeta {
		return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	created, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
		CommandMeta: meta("installed-generation-create"), TaskID: taskID, RevisionID: revisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.prepareAndBindManagedAttempt(context.Background(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
		return server.service.PrepareRun(prepareCtx, app.PrepareRunRequest{
			CommandMeta: meta("installed-generation-prepare"), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
		})
	})
	if !errors.Is(err, generationErr) {
		t.Fatalf("installed Pi Docker preparation error = %v, want generation authority failure", err)
	}
	if prepared.Attempt.AgentExecutionProfileBinding().ExecutionProvider() != domain.DockerExecutionProvider {
		t.Fatalf("installed Attempt provider = %q, want Docker", prepared.Attempt.AgentExecutionProfileBinding().ExecutionProvider())
	}
	if _, err := server.startManagedAttempt(context.Background(), prepared.Attempt, app.StartAttemptRequest{
		CommandMeta: meta("installed-generation-start"), RunID: created.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	}); !errors.Is(err, generationErr) {
		t.Fatalf("installed Pi Docker start error = %v, want generation authority failure", err)
	}
	assertTableCounts(t, databasePath, map[string]int{"runs": 1, "attempts": 1, "runtime_sessions": 0})
}

func TestManagedStartFailsBeforeRunSideEffectsWhenReferencePublicationIsUncertain(t *testing.T) {
	server, databasePath := newAgentExecutionProductServer(t)
	handler := server.Handler()
	task, revisionID, roomID := createAcceptedManagedProfileTask(t, handler)
	publisher := server.generationPublisher.(*recordingGenerationPublisher)
	publisher.err = errors.New("token sk-reference-secret publish unavailable")
	body, err := json.Marshal(map[string]any{"revisionId": revisionID})
	if err != nil {
		t.Fatal(err)
	}
	request := newLocalRequest(http.MethodPost, "/api/tasks/"+task.ID+"/runs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "blocked-generation-publication")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "sk-reference-secret") {
		t.Fatalf("managed publication failure response code=%d body=%s", response.Code, response.Body.String())
	}
	assertTableCounts(t, databasePath, map[string]int{"runs": 0, "attempts": 0, "runtime_sessions": 0})
	var fetched taskRefView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+roomID+"/tasks/"+task.ID, nil, http.StatusOK, &fetched)
	if fetched.AgentExecutionProfile != string(domain.AgentExecutionProfileStandard) {
		t.Fatalf("blocked start mutated Task profile=%q", fetched.AgentExecutionProfile)
	}
}

func TestManagedAttemptPreparationSerializesWithDestructiveLifecyclePlanning(t *testing.T) {
	for _, action := range []string{"GC", "Uninstall"} {
		t.Run(action, func(t *testing.T) {
			server, _ := newAgentExecutionProductServer(t)
			task, revisionText, _ := createAcceptedManagedProfileTask(t, server.Handler())
			taskID, err := domain.ParseTaskID(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
			if err != nil {
				t.Fatal(err)
			}
			stateRoot := filepath.Join(canonicalPrivateTestRoot(t), "install-state")
			backend, err := productinstall.NewFileBackend(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			setActiveInstallationGeneration(t, backend, "generation-g1")
			server.installationGenerationID = "generation-g1"
			server.generationPublisher = backend

			meta := func(key string) app.CommandMeta {
				return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
			}
			created, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
				CommandMeta: meta("prepare-fence-create-" + strings.ToLower(action)), TaskID: taskID, RevisionID: revisionID,
			})
			if err != nil {
				t.Fatal(err)
			}

			prepareEntered := make(chan struct{})
			releasePrepare := make(chan struct{})
			prepareDone := make(chan error, 1)
			go func() {
				_, prepareErr := server.prepareAndBindManagedAttempt(context.Background(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
					close(prepareEntered)
					<-releasePrepare
					return server.service.PrepareRun(prepareCtx, app.PrepareRunRequest{
						CommandMeta: meta("prepare-fence-attempt-" + strings.ToLower(action)), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
					})
				})
				prepareDone <- prepareErr
			}()
			<-prepareEntered

			independent, err := productinstall.NewFileBackend(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			plannerCtx, cancelPlanner := context.WithCancel(context.Background())
			plannerAttempted := make(chan struct{})
			plannerEntered := make(chan struct{}, 1)
			plannerDone := make(chan error, 1)
			go func() {
				close(plannerAttempted)
				plannerDone <- independent.WithInstallationLock(plannerCtx, func(context.Context) error {
					plannerEntered <- struct{}{}
					return nil
				})
			}()
			<-plannerAttempted
			cancelPlanner()
			if err := <-plannerDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s planning was not fenced by preparation: %v", action, err)
			}
			select {
			case <-plannerEntered:
				t.Fatalf("%s planning entered while managed preparation held install.lock", action)
			default:
			}

			close(releasePrepare)
			if err := <-prepareDone; err != nil {
				t.Fatal(err)
			}
			assertGenerationReferences(t, backend, []string{"generation-g1"}, []string{"generation-g1"}, nil)
		})
	}
}

func TestManagedStartFailsClosedAfterUpgradeOrUninstallUntilRestart(t *testing.T) {
	for _, test := range []struct {
		name              string
		durableGeneration string
	}{
		{name: "Upgrade", durableGeneration: "generation-g2"},
		{name: "Uninstall", durableGeneration: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, databasePath := newAgentExecutionProductServer(t)
			task, revisionText, _ := createAcceptedManagedProfileTask(t, server.Handler())
			taskID, err := domain.ParseTaskID(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
			if err != nil {
				t.Fatal(err)
			}
			meta := func(key string) app.CommandMeta {
				return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
			}
			created, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
				CommandMeta: meta("restart-gate-create-" + strings.ToLower(test.name)), TaskID: taskID, RevisionID: revisionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := server.prepareAndBindManagedAttempt(context.Background(), func(prepareCtx context.Context) (app.PrepareRunResult, error) {
				return server.service.PrepareRun(prepareCtx, app.PrepareRunRequest{
					CommandMeta: meta("restart-gate-prepare-" + strings.ToLower(test.name)), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			publisher := server.generationPublisher.(*recordingGenerationPublisher)
			publisher.setActiveGeneration(test.durableGeneration)
			if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			publisher.mu.Lock()
			latest := publisher.snapshots[len(publisher.snapshots)-1]
			publisher.mu.Unlock()
			if !slices.Contains(latest.ServingGenerationIDs, "test-generation") {
				t.Fatalf("process-loaded generation was not protected after %s: %+v", test.name, latest)
			}
			_, err = server.startManagedAttempt(context.Background(), prepared.Attempt, app.StartAttemptRequest{
				CommandMeta: meta("restart-gate-start-" + strings.ToLower(test.name)), RunID: created.Run.ID(),
				ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
			})
			if err == nil || !strings.Contains(err.Error(), "restart is required") {
				t.Fatalf("managed start after %s error = %v", test.name, err)
			}
			assertTableCounts(t, databasePath, map[string]int{"runs": 1, "attempts": 1, "runtime_sessions": 0})
		})
	}
}

func TestFailedPreDeactivationUninstallThenUpgradeKeepsServingGenerationProtectedFromGC(t *testing.T) {
	server, _ := newAgentExecutionProductServer(t)
	stateRoot := filepath.Join(canonicalPrivateTestRoot(t), "failed-uninstall-upgrade-state")
	backend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	target := productinstall.EngineTarget{
		CLIPath: "/private/test/docker", ContextName: "chora-test", EndpointDigest: strings.Repeat("e", 64),
	}
	ports := &generationLifecycleTestPorts{target: target, inventory: map[string]bool{}}
	service := generationLifecycleService(t, backend, backend, ports)
	g1 := generationLifecycleSpec("g1", "a")
	if result, err := service.Setup(context.Background(), productinstall.InstallRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionSetup), Key: "setup-g1", Target: target, Generation: g1,
	}); err != nil || !result.Active {
		t.Fatalf("Setup g1 = (%+v, %v)", result, err)
	}
	server.installationGenerationID = "g1"
	server.generationPublisher = backend
	server.generationConfigErr = nil
	if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, backend, []string{"g1"}, nil, nil)

	failing := generationLifecycleService(t, backend, failingGenerationDeactivator{backend: backend}, ports)
	_, err = failing.Uninstall(context.Background(), productinstall.UninstallRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionUninstall), Key: "uninstall-g1-fails", Target: target,
	})
	if !errors.Is(err, productinstall.ErrOperationFailed) {
		t.Fatalf("pre-deactivation Uninstall error = %v", err)
	}
	state, err := backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failed := state.Operations[len(state.Operations)-1]
	if state.ActiveGenerationID != "g1" || failed.Phase != productinstall.PhaseFailed || failed.RetiredServingGenerationID != "" {
		t.Fatalf("failed-before-deactivation journal = active=%q operation=%+v", state.ActiveGenerationID, failed)
	}

	g2 := generationLifecycleSpec("g2", "b")
	if result, err := service.Upgrade(context.Background(), productinstall.InstallRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionUpgrade), Key: "upgrade-g2", Target: target, Generation: g2,
	}); err != nil || !result.Active {
		t.Fatalf("Upgrade g2 = (%+v, %v)", result, err)
	}
	if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, backend, []string{"g1"}, nil, nil)

	collected, err := service.GarbageCollect(context.Background(), productinstall.GCRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionGarbageGC), Key: "gc-after-failed-uninstall", Target: target, Limit: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(collected.SkippedGenerationIDs, []string{"g1"}) || len(collected.RemovedAssetIDs) != 0 {
		t.Fatalf("GC after failed Uninstall/Upgrade = %+v", collected)
	}
	state, err = backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveGenerationID != "g2" || state.Generations[0].Status != productinstall.GenerationSuperseded || !ports.inventory[g1.Assets[0].Identity] {
		t.Fatalf("GC invalidated serving g1: state=%+v inventory=%v", state, ports.inventory)
	}
}

func TestCrashBeforeServingRetirementProofRepublishesUntilReplayPersistsProof(t *testing.T) {
	server, _ := newAgentExecutionProductServer(t)
	stateRoot := filepath.Join(canonicalPrivateTestRoot(t), "retirement-proof-crash-state")
	backend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	target := productinstall.EngineTarget{
		CLIPath: "/private/test/docker", ContextName: "chora-test", EndpointDigest: strings.Repeat("e", 64),
	}
	ports := &generationLifecycleTestPorts{target: target, inventory: map[string]bool{}}
	service := generationLifecycleService(t, backend, backend, ports)
	g1 := generationLifecycleSpec("g1", "a")
	if _, err := service.Setup(context.Background(), productinstall.InstallRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionSetup), Key: "proof-crash-setup", Target: target, Generation: g1,
	}); err != nil {
		t.Fatal(err)
	}
	server.installationGenerationID = "g1"
	server.generationPublisher = backend
	server.generationConfigErr = nil
	if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	store := &failRetirementProofStore{backend: backend}
	crashing := generationLifecycleServiceWithStore(t, backend, store, backend, ports)
	request := productinstall.UninstallRequest{
		Authority: generationLifecycleAuthority(productinstall.ActionUninstall), Key: "proof-crash-uninstall", Target: target,
	}
	if _, err := crashing.Uninstall(context.Background(), request); !errors.Is(err, errRetirementProofCheckpoint) {
		t.Fatalf("retirement proof crash error = %v", err)
	}
	state, err := backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation := state.Operations[len(state.Operations)-1]
	if state.ActiveGenerationID != "" || operation.Phase != productinstall.PhaseRemoving || operation.RetiredServingGenerationID != "" {
		t.Fatalf("durable crash gap = active=%q operation=%+v", state.ActiveGenerationID, operation)
	}
	assertGenerationReferences(t, backend, nil, nil, nil)

	if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, backend, []string{"g1"}, nil, nil)
	result, err := service.Uninstall(context.Background(), request)
	if err != nil || !result.Replayed {
		t.Fatalf("retirement proof replay = (%+v, %v)", result, err)
	}
	state, err = backend.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation = state.Operations[len(state.Operations)-1]
	if operation.RetiredServingGenerationID != "g1" {
		t.Fatalf("replay did not persist exact retirement proof: %+v", operation)
	}
	assertGenerationReferences(t, backend, nil, nil, nil)
}

func TestManagedGenerationReferencesReloadRecoverAndClearOnlyAfterStoreProof(t *testing.T) {
	server, databasePath := newAgentExecutionProductServer(t)
	handler := server.Handler()
	task, revisionText, _ := createAcceptedManagedProfileTask(t, handler)
	taskID, err := domain.ParseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(revisionText)
	if err != nil {
		t.Fatal(err)
	}
	stateParent := t.TempDir()
	if err := os.Chmod(stateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	stateParent, err = filepath.EvalSymlinks(stateParent)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(stateParent, "install-state")
	backend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	setActiveInstallationGeneration(t, backend, "generation-g1")
	server.installationGenerationID = "generation-g1"
	server.generationPublisher = backend
	server.generationConfigErr = nil
	meta := func(key string) app.CommandMeta {
		return app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	created, err := server.service.CreateRun(context.Background(), app.CreateRunRequest{
		CommandMeta: meta("generation-create-run"), TaskID: taskID, RevisionID: revisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.service.PrepareRun(context.Background(), app.PrepareRunRequest{
		CommandMeta: meta("generation-prepare-run"), RunID: created.Run.ID(), ExpectedVersion: created.Run.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.bindAndPublishManagedAttempt(context.Background(), prepared.Attempt); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, backend, []string{"generation-g1"}, []string{"generation-g1"}, nil)
	started, err := server.service.StartAttempt(context.Background(), app.StartAttemptRequest{
		CommandMeta: meta("generation-start-run"), RunID: created.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
	})
	if err != nil || started.Session == nil {
		t.Fatalf("start=%#v err=%v", started, err)
	}
	if err := server.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := storesqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopenedBackend, err := productinstall.NewFileBackend(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	setActiveInstallationGeneration(t, reopenedBackend, "generation-g2")
	restarted := &Server{
		ctx: context.Background(), store: reopenedStore, product: true, generationPublisher: reopenedBackend,
		installationGenerationID: "generation-g2", logger: log.New(io.Discard, "", 0),
	}
	if err := restarted.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, reopenedBackend, []string{"generation-g2"}, []string{"generation-g1"}, nil)
	if generationID, found, err := managedGenerationForAttempt(context.Background(), reopenedStore.Reader(), created.Run.ID(), prepared.Attempt.ID()); err != nil || !found || generationID != "generation-g1" {
		t.Fatalf("reloaded generation binding=%q found=%v err=%v", generationID, found, err)
	}

	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE runs SET state='recovery_required' WHERE id=?`, created.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, reopenedBackend, []string{"generation-g2"}, nil, []string{"generation-g1"})

	payload, _ := json.Marshal(managedGenerationBinding{SchemaVersion: managedGenerationBindingSchema, AttemptID: prepared.Attempt.ID().String(), GenerationID: "generation-g1"})
	if err := server.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		now := time.Now().UTC()
		_, err := tx.AppendRunEvent(context.Background(), created.Run.ID(), storecontract.EventDraft{
			ID: domain.NewEventID(), Type: managedGenerationBindingEvent, Source: managedGenerationBindingSource,
			OccurredAt: now, RecordedAt: now, NormalizedJSON: payload,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publishManagedGenerationReferences(context.Background(), nil); err == nil {
		t.Fatal("duplicate managed generation binding did not block publication")
	}
	assertGenerationReferences(t, reopenedBackend, []string{"generation-g2"}, nil, []string{"generation-g1"})

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.Exec(`UPDATE runs SET state='awaiting_verification' WHERE id=?`, created.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE attempts SET state='output_submitted' WHERE id=?`, prepared.Attempt.ID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE runtime_sessions SET state='stopped',terminal_at=?,finalized_at=? WHERE attempt_id=?`, now, now, prepared.Attempt.ID().String()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.publishManagedGenerationReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	assertGenerationReferences(t, reopenedBackend, []string{"generation-g2"}, nil, nil)
}

func setActiveInstallationGeneration(t *testing.T, backend *productinstall.FileBackend, generationID string) {
	t.Helper()
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
	state.ActiveGenerationID = generationID
	expected := state.Revision
	state.Revision++
	if err := backend.SaveCAS(context.Background(), expected, state); err != nil {
		t.Fatal(err)
	}
}

func canonicalPrivateTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

type generationLifecycleTestPorts struct {
	target    productinstall.EngineTarget
	inventory map[string]bool
	removed   []string
}

func (ports *generationLifecycleTestPorts) Now() time.Time { return time.Unix(10, 0).UTC() }
func (ports *generationLifecycleTestPorts) Observe(context.Context, productinstall.EngineTarget) (productinstall.EngineObservation, error) {
	return productinstall.EngineObservation{
		DaemonID: "daemon-test", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextName: ports.target.ContextName, EndpointDigest: ports.target.EndpointDigest,
	}, nil
}
func (ports *generationLifecycleTestPorts) Authorize(_ context.Context, authority productinstall.MutationAuthority, action productinstall.Action) error {
	if authority.Action != action {
		return productinstall.ErrAuthorityRequired
	}
	return nil
}
func (ports *generationLifecycleTestPorts) InspectExact(_ context.Context, _ productinstall.EngineTarget, asset productinstall.AssetSpec) (productinstall.AssetStatus, error) {
	if ports.inventory[asset.Identity] {
		return productinstall.AssetStatus{Present: true, Identity: asset.Identity}, nil
	}
	return productinstall.AssetStatus{}, nil
}
func (ports *generationLifecycleTestPorts) AcquirePinned(_ context.Context, _ productinstall.EngineTarget, asset productinstall.AssetSpec) error {
	ports.inventory[asset.Identity] = true
	return nil
}
func (ports *generationLifecycleTestPorts) RemoveExact(_ context.Context, _ productinstall.EngineTarget, asset productinstall.AssetSpec) error {
	ports.removed = append(ports.removed, asset.Identity)
	delete(ports.inventory, asset.Identity)
	return nil
}
func (ports *generationLifecycleTestPorts) Probe(context.Context, productinstall.MutationAuthority, productinstall.EngineTarget, productinstall.GenerationSpec) (productinstall.ProbeResult, error) {
	return productinstall.ProbeResult{
		Passed: true, CleanupProven: true, QualificationDigest: strings.Repeat("f", 64),
		Engine: productinstall.EngineObservation{
			DaemonID: "daemon-test", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
			ContextName: ports.target.ContextName, EndpointDigest: ports.target.EndpointDigest,
		},
	}, nil
}
func (*generationLifecycleTestPorts) Verify(context.Context, productinstall.EngineTarget, productinstall.GenerationSpec, []productinstall.InstalledAsset, productinstall.ProbeResult) error {
	return nil
}

type failingGenerationDeactivator struct {
	backend *productinstall.FileBackend
}

var errRetirementProofCheckpoint = errors.New("injected retirement proof checkpoint failure")

type failRetirementProofStore struct {
	backend *productinstall.FileBackend
	failed  bool
}

func (store *failRetirementProofStore) Load(ctx context.Context) (productinstall.State, error) {
	return store.backend.Load(ctx)
}
func (store *failRetirementProofStore) SaveCAS(ctx context.Context, expected uint64, state productinstall.State) error {
	if !store.failed {
		for _, operation := range state.Operations {
			if operation.RetiredServingGenerationID != "" {
				store.failed = true
				return errRetirementProofCheckpoint
			}
		}
	}
	return store.backend.SaveCAS(ctx, expected, state)
}

func (activator failingGenerationDeactivator) Current(ctx context.Context) (string, error) {
	return activator.backend.Current(ctx)
}
func (activator failingGenerationDeactivator) AtomicActivate(ctx context.Context, expected string, generation productinstall.GenerationSpec) error {
	return activator.backend.AtomicActivate(ctx, expected, generation)
}
func (failingGenerationDeactivator) AtomicDeactivate(context.Context, string) error {
	return errors.New("injected deactivation failure")
}

func generationLifecycleService(t *testing.T, backend *productinstall.FileBackend, activator productinstall.Activator, ports *generationLifecycleTestPorts) *productinstall.Service {
	return generationLifecycleServiceWithStore(t, backend, backend, activator, ports)
}

func generationLifecycleServiceWithStore(t *testing.T, backend *productinstall.FileBackend, store productinstall.StateStore, activator productinstall.Activator, ports *generationLifecycleTestPorts) *productinstall.Service {
	t.Helper()
	service, err := productinstall.New(productinstall.Dependencies{
		Clock: ports, Locker: backend, Observer: ports, Authorizer: ports, Store: store,
		Inspector: ports, Acquirer: ports, Remover: ports, Prober: ports, Verifier: ports,
		Activator: activator, References: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func generationLifecycleSpec(generationID, identityCharacter string) productinstall.GenerationSpec {
	asset := productinstall.AssetSpec{
		ID: "agent-" + generationID, Kind: "oci_image", Identity: "sha256:" + strings.Repeat(identityCharacter, 64),
		Ownership: productinstall.OwnershipChora, AcquisitionKind: productinstall.AcquisitionLocalDockerArchive,
		ArchivePath: "/private/chora/releases/" + generationID + ".tar", ArchiveSize: 10240,
		ArchiveSHA256: strings.Repeat(identityCharacter, 64),
	}
	return productinstall.GenerationSpec{
		SchemaVersion: productinstall.GenerationSchema, GenerationID: generationID, ReleaseID: "release-" + generationID,
		ManifestSHA256: strings.Repeat(identityCharacter, 64), Assets: []productinstall.AssetSpec{asset},
	}
}

func generationLifecycleAuthority(action productinstall.Action) productinstall.MutationAuthority {
	return productinstall.MutationAuthority{
		GrantID: "grant-" + string(action), ActorID: "actor", SessionID: "session", Action: action, IssuedAt: time.Unix(9, 0).UTC(),
	}
}

func createAcceptedManagedProfileTask(t *testing.T, handler http.Handler) (taskRefView, string, string) {
	t.Helper()
	var createdRoom struct {
		ID string `json:"id"`
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms", map[string]any{
		"name": "Generation references", "description": "Bind installed generation to managed Attempts.",
	}, map[string]string{"Idempotency-Key": "create-generation-room"}, http.StatusCreated, &createdRoom)
	var room roomDetailView
	requestJSON(t, handler, http.MethodGet, "/api/rooms/"+createdRoom.ID, nil, http.StatusOK, &room)
	body := realTaskAPIBody(room.Revisions[0].ID, "Publish the exact installed generation")
	body["agentExecutionProfile"] = string(domain.AgentExecutionProfileStandard)
	var task taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/rooms/"+room.ID+"/tasks", body,
		map[string]string{"Idempotency-Key": "create-generation-task"}, http.StatusCreated, &task)
	acceptTaskPlan(t, handler, task.ID)
	requestJSON(t, handler, http.MethodGet, "/api/tasks/"+task.ID, nil, http.StatusOK, &task)
	if task.Planning.Acceptance == nil {
		t.Fatalf("managed Task planning not accepted: %#v", task.Planning)
	}
	return task, task.Planning.Acceptance.RevisionID, room.ID
}

func assertGenerationReferences(t *testing.T, backend *productinstall.FileBackend, serving, active, recoverable []string) {
	t.Helper()
	refs, err := backend.References(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(refs.ServingGenerationIDs, serving) || !slices.Equal(refs.ActiveAttemptGenerationIDs, active) || !slices.Equal(refs.RecoverableAttemptGenerationIDs, recoverable) {
		t.Fatalf("generation refs serving=%v active=%v recoverable=%v, want %v/%v/%v", refs.ServingGenerationIDs, refs.ActiveAttemptGenerationIDs, refs.RecoverableAttemptGenerationIDs, serving, active, recoverable)
	}
}
