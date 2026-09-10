package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestAcceptRealSpecCodingPlanRollsBackEveryResourceAfterSnapshotFailure(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	created, err := service.CreateTask(ctx, realSpecCodingCreateRequest(room))
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: meta("submit-rollback-plan", "submit-rollback-plan"), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := speccoding.LoadInstalledEnvelope(room.repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	failing := app.NewService(app.Dependencies{
		Store: db, Context: saveSnapshotThenFail{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
		InstalledSpecCodingEnvelope: &envelope,
	})
	if _, err := failing.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta("accept-rollback-plan", "accept-rollback-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Must roll back.",
	}); !errors.Is(err, errInjectedSnapshotFailure) {
		t.Fatalf("injected acceptance error = %v", err)
	}
	assertRawCounts(t, raw, map[string]int{
		"run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0,
		"technical_plan_reviews": 0, "technical_plan_acceptance_bindings": 0, "runs": 0,
	})
}

func TestAcceptRealSpecCodingPlanRejectsPersistedIntentTamperAfterRestart(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	created, err := service.CreateTask(ctx, realSpecCodingCreateRequest(room))
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: meta("submit-tampered-intent", "submit-tampered-intent"), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TRIGGER user_spec_coding_intents_no_update`); err != nil {
		t.Fatal(err)
	}
	tampered := []byte(`{"unexpected":"authority"}`)
	digest := sha256.Sum256(tampered)
	if _, err := raw.ExecContext(ctx, `UPDATE user_spec_coding_intents SET intent_json=?,intent_digest=? WHERE task_id=?`, tampered, digest[:], created.Task.ID().String()); err != nil {
		t.Fatal(err)
	}
	restarted, _ := restartedRealSpecCodingService(t, db, room)
	if _, err := restarted.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta("accept-tampered-intent", "accept-tampered-intent"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Tamper must fail closed.",
	}); !errors.Is(err, storecontract.ErrSpecCodingConflict) {
		t.Fatalf("tampered intent acceptance error = %v", err)
	}
	assertRawCounts(t, raw, map[string]int{
		"run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0,
		"technical_plan_reviews": 0, "technical_plan_acceptance_bindings": 0, "runs": 0,
	})
}

func TestAcceptRealSpecCodingPlanRejectsSelectionTamperWithoutAllocatingResources(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	created, err := service.CreateTask(ctx, realSpecCodingCreateRequest(room))
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: meta("submit-tampered-selection", "submit-tampered-selection"), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TRIGGER task_revision_selections_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE task_revision_selections SET digest=? WHERE task_id=?`, bytes.Repeat([]byte{0x7a}, 32), created.Task.ID().String()); err != nil {
		t.Fatal(err)
	}
	restarted, _ := restartedRealSpecCodingService(t, db, room)
	if _, err := restarted.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta("accept-tampered-selection", "accept-tampered-selection"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Selection tamper must fail closed.",
	}); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("tampered selection acceptance error = %v", err)
	}
	assertRawCounts(t, raw, map[string]int{
		"run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0,
		"technical_plan_reviews": 0, "technical_plan_acceptance_bindings": 0, "runs": 0,
	})
}

func TestTwoRealSpecCodingTasksHaveDistinctIntentAndAcceptanceIdentities(t *testing.T) {
	ctx := context.Background()
	service, db, _, room := realSpecCodingAppFixture(t, ctx)
	firstRequest := realSpecCodingCreateRequest(room)
	first, err := service.CreateTask(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := realSpecCodingCreateRequest(room)
	secondRequest.CommandMeta = meta("create-real-task-two", "create-real-task-two")
	secondRequest.Title = "Preserve valid descriptions"
	second, err := service.CreateTask(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstIntent, err := db.Reader().GetSpecCodingIntent(ctx, first.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	secondIntent, err := db.Reader().GetSpecCodingIntent(ctx, second.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.ID() == second.Task.ID() || first.Draft.ID() == second.Draft.ID() || firstIntent.IntentDigest == secondIntent.IntentDigest || bytes.Equal(firstIntent.IntentJSON, secondIntent.IntentJSON) {
		t.Fatal("two user-created Real Tasks reused Task, Draft, or immutable intent identity")
	}

	firstAccepted := submitAndAcceptRealTask(t, ctx, service, first, "first")
	secondAccepted := submitAndAcceptRealTask(t, ctx, service, second, "second")
	if firstAccepted.Charter.ID() == secondAccepted.Charter.ID() || firstAccepted.Snapshot.ID() == secondAccepted.Snapshot.ID() || firstAccepted.Snapshot.Digest() == secondAccepted.Snapshot.Digest() {
		t.Fatal("two user-created Real Tasks reused Charter or Snapshot authority")
	}
}

var errInjectedSnapshotFailure = errors.New("injected failure after Snapshot persistence")

type saveSnapshotThenFail struct{}

func (saveSnapshotThenFail) Assemble(ctx context.Context, port contextcore.SnapshotPort, request contextcore.AssembleRequest) (contextcore.Snapshot, error) {
	if _, err := (app.ContextAssembler{}).Assemble(ctx, port, request); err != nil {
		return contextcore.Snapshot{}, err
	}
	return contextcore.Snapshot{}, errInjectedSnapshotFailure
}

func TestAcceptRealSpecCodingPlanAfterRestartAtomicallyBindsExecution(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	request := realSpecCodingCreateRequest(room)
	created, err := service.CreateTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: meta("submit-real-plan", "submit-real-plan"), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted, envelope := restartedRealSpecCodingService(t, db, room)
	reviewRequest := app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta("accept-real-plan", "accept-real-plan"), RevisionID: submitted.Revision.ID(),
		Kind: domain.TechnicalPlanReviewAccept, Note: "The exact declared Plan is accepted.",
	}
	accepted, err := restarted.ReviewTechnicalPlanRevision(ctx, reviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Binding == nil || accepted.Charter == nil || accepted.Snapshot == nil {
		t.Fatalf("accepted resources = %#v", accepted)
	}
	if accepted.Charter.AdapterID() != "pi" || accepted.Charter.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileStandard || accepted.Charter.SandboxMode() != "colima-docker" || accepted.Charter.WorkspaceRoot() != mustRoomRoot(t, db, room) || !sameCapabilities(accepted.Charter.CapabilityEnvelope(), envelope.CapabilityEnvelope()) {
		t.Fatalf("accepted Charter = %#v", accepted.Charter)
	}
	if accepted.Binding.SnapshotID() != accepted.Snapshot.ID() || accepted.Binding.SnapshotDigest() != accepted.Snapshot.Digest() || sha256.Sum256(accepted.Snapshot.CanonicalJSON()) != accepted.Snapshot.Digest() {
		t.Fatalf("accepted planning binding = %#v", accepted.Binding)
	}
	specBinding, err := db.Reader().GetSpecCodingBinding(ctx, created.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if specBinding.Status != storecontract.SpecCodingRegistered || specBinding.SnapshotID != accepted.Snapshot.ID() || specBinding.SnapshotDigest != accepted.Snapshot.Digest() || specBinding.MaterializedContractDigest != specBinding.ActiveContractDigest || !bytes.Equal(specBinding.MaterializedContractJSON, specBinding.ActiveContractJSON) {
		t.Fatalf("registered Spec Coding binding = %#v", specBinding)
	}
	contract, err := speccoding.DecodeCoreContract(specBinding.ActiveContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	snapshotDigest := accepted.Snapshot.Digest()
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV10 || document.Revision != 10 || document.Candidate.Runtime.Version != "0.84.2" || document.Task.ID != created.Task.ID().String() || document.Task.RoomID != created.Task.RoomID().String() || document.Execution.Input.ContextSnapshotID != accepted.Snapshot.ID().String() || document.Execution.Input.ContextSnapshotDigest != hex.EncodeToString(snapshotDigest[:]) {
		t.Fatalf("active v8 contract identity = %#v", document)
	}
	if !equalStrings(document.Execution.Boundary.WritableFiles, request.RealSpecCoding.WritableFiles) || !equalStrings(document.Acceptance.VerificationCommands[0].Argv, request.RealSpecCoding.VerificationCommands[0].Argv) {
		t.Fatalf("active v8 authority = %#v", document.Execution.Boundary)
	}
	assertRawCounts(t, raw, map[string]int{
		"run_charters": 1, "context_snapshots": 1, "spec_coding_bindings": 1,
		"technical_plan_reviews": 1, "technical_plan_acceptance_bindings": 1, "runs": 0,
	})

	replay, err := restarted.ReviewTechnicalPlanRevision(ctx, reviewRequest)
	if err != nil || !replay.Replayed || replay.Charter == nil || replay.Snapshot == nil || replay.Charter.ID() != accepted.Charter.ID() || replay.Snapshot.ID() != accepted.Snapshot.ID() {
		t.Fatalf("acceptance replay = %#v, err = %v", replay, err)
	}
	changed := reviewRequest
	changed.Note = "Changed acceptance meaning must conflict."
	if _, err := restarted.ReviewTechnicalPlanRevision(ctx, changed); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("changed acceptance key error = %v", err)
	}

	createdRun, err := restarted.CreateRun(ctx, app.CreateRunRequest{
		CommandMeta: meta("create-real-run", "create-real-run"), TaskID: created.Task.ID(), RevisionID: submitted.Revision.ID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdRun.Binding.CharterID() != accepted.Charter.ID() || createdRun.Binding.SnapshotID() != accepted.Snapshot.ID() || createdRun.Binding.SnapshotDigest() != accepted.Snapshot.Digest() {
		t.Fatalf("Run substituted accepted resources: %#v", createdRun.Binding)
	}
}

func TestStartRealSpecCodingAttemptReceivesExactRegisteredContract(t *testing.T) {
	ctx := context.Background()
	service, db, _, room := realSpecCodingAppFixture(t, ctx)
	created, err := service.CreateTask(ctx, realSpecCodingCreateRequest(room))
	if err != nil {
		t.Fatal(err)
	}
	accepted := submitAndAcceptRealTask(t, ctx, service, created, "execution-input")
	registered, err := db.Reader().GetSpecCodingBinding(ctx, created.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{id: "pi"}
	supervisor := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:real"}}}
	envelope, err := speccoding.LoadInstalledEnvelope(room.repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	executor := app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{adapter}, Supervisor: supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, InstalledSpecCodingEnvelope: &envelope,
	})
	run, err := executor.CreateRun(ctx, app.CreateRunRequest{
		CommandMeta: meta("create-real-execution-input-run", "create-real-execution-input-run"), TaskID: created.Task.ID(), RevisionID: accepted.Binding.RevisionID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := executor.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-real-execution-input", "prepare-real-execution-input"), RunID: run.Run.ID(), ExpectedVersion: run.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := db.Reader().GetCharter(ctx, run.Run.CharterID())
	if err != nil || prepared.Attempt.AgentExecutionProfileBinding() != charter.AgentExecutionProfileBinding() || prepared.Attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileStandard {
		t.Fatalf("first Attempt profile=%#v Charter profile=%#v err=%v", prepared.Attempt.AgentExecutionProfileBinding(), charter.AgentExecutionProfileBinding(), err)
	}
	if _, err := executor.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("start-real-execution-input", "start-real-execution-input"), RunID: run.Run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(adapter.startRequest.ExecutionContractDocument, registered.ActiveContractJSON) || !bytes.Equal(adapter.startRequest.SnapshotDocument, accepted.Snapshot.CanonicalJSON()) {
		t.Fatal("Pi start did not receive the exact registered Contract and accepted Snapshot")
	}
	if adapter.prepareCalls != 1 || supervisor.invocation.Target().AdapterID() != "pi" || supervisor.invocation.Target().ProviderID() != domain.DockerExecutionProvider {
		t.Fatalf("atomic bound prepare calls=%d target=%q/%q", adapter.prepareCalls, supervisor.invocation.Target().AdapterID(), supervisor.invocation.Target().ProviderID())
	}
}

func TestCreateRealSpecCodingTaskPersistsOnlyDeclarationAtomically(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	request := realSpecCodingCreateRequest(room)

	created, err := service.CreateTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.ExecutionProfile != app.TaskExecutionProfileRealSpecCoding || created.Task.Goal() != request.RealSpecCoding.Requirement || created.Draft.Content().TechnicalSteps[0] == "" {
		t.Fatalf("created Real Task = %#v", created)
	}
	intent, err := db.Reader().GetSpecCodingIntent(ctx, created.Task.ID())
	if err != nil || intent.TaskID != created.Task.ID() || intent.IntentDigest == ([32]byte{}) || len(intent.IntentJSON) == 0 {
		t.Fatalf("persisted intent = %#v, err = %v", intent, err)
	}
	assertRawCounts(t, raw, map[string]int{
		"tasks": 1, "task_revision_selections": 1, "technical_plan_drafts": 1,
		"user_spec_coding_intents": 1, "run_charters": 0, "context_snapshots": 0,
		"spec_coding_bindings": 0, "technical_plan_reviews": 0,
		"technical_plan_acceptance_bindings": 0, "runs": 0,
	})

	replay, err := service.CreateTask(ctx, request)
	if err != nil || !replay.Replayed || replay.Task.ID() != created.Task.ID() || replay.Draft.ID() != created.Draft.ID() || replay.ExecutionProfile != app.TaskExecutionProfileRealSpecCoding {
		t.Fatalf("exact replay = %#v, err = %v", replay, err)
	}
	changed := request
	changed.RealSpecCoding.Requirement = "A changed requirement must conflict."
	if _, err := service.CreateTask(ctx, changed); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("changed key reuse error = %v", err)
	}
	assertRawCounts(t, raw, map[string]int{"tasks": 1, "user_spec_coding_intents": 1, "run_charters": 0, "context_snapshots": 0, "spec_coding_bindings": 0, "runs": 0})
}

func TestCreateRealSpecCodingTaskRejectsUnsupportedInputWithoutPartialTask(t *testing.T) {
	ctx := context.Background()
	service, _, raw, room := realSpecCodingAppFixture(t, ctx)
	request := realSpecCodingCreateRequest(room)
	request.RealSpecCoding.WritableFiles[0] = "../escape.go"

	if _, err := service.CreateTask(ctx, request); !errors.Is(err, speccoding.ErrUnsupportedUserTask) {
		t.Fatalf("unsupported declaration error = %v", err)
	}
	assertRawCounts(t, raw, map[string]int{
		"tasks": 0, "task_revision_selections": 0, "technical_plan_drafts": 0,
		"user_spec_coding_intents": 0, "run_charters": 0, "context_snapshots": 0,
		"spec_coding_bindings": 0, "runs": 0,
	})
}

type realSpecCodingRoom struct {
	id             string
	revisionID     string
	repositoryRoot string
}

func realSpecCodingAppFixture(t *testing.T, ctx context.Context) (*app.Service, storecontract.Store, *sql.DB, realSpecCodingRoom) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := speccoding.LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "chora.db")
	db, err := openLatestSQLiteTestStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	service := app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
		InstalledSpecCodingEnvelope: &envelope,
	})
	createdRoom, err := service.CreateRoom(ctx, app.CreateRoomRequest{
		CommandMeta: meta("create-real-room", "create-real-room"), Name: "chora", Description: "Installed Chora source baseline", WorkspaceRoot: speccoding.InstalledWorkspaceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, raw, realSpecCodingRoom{id: createdRoom.Room.ID().String(), revisionID: createdRoom.InitialRevision.ID().String(), repositoryRoot: repositoryRoot}
}

func realSpecCodingCreateRequest(room realSpecCodingRoom) app.CreateTaskRequest {
	roomID, _ := domain.ParseRoomID(room.id)
	revisionID, _ := domain.ParseContextRevisionID(room.revisionID)
	return app.CreateTaskRequest{
		CommandMeta: meta("create-real-task", "create-real-task"), RoomID: roomID,
		Title: "Require reviewable descriptions", ExecutionProfile: app.TaskExecutionProfileRealSpecCoding,
		RevisionIDs: []domain.ContextRevisionID{revisionID},
		RealSpecCoding: &app.RealSpecCodingInput{
			Requirement: "Reject blank acceptance descriptions.",
			Constraints: []string{"Preserve valid criteria."}, OutOfScope: []string{"Persistence changes."},
			Criteria:             []app.RealSpecCodingCriterion{{Title: "Blank descriptions fail", Description: "Empty descriptions are rejected.", VerificationCommandIndexes: []int{0}}},
			WritableFiles:        []string{"internal/domain/task.go", "internal/domain/task_test.go"},
			VerificationCommands: []app.RealSpecCodingVerificationCommand{{Argv: []string{"go", "test", "./internal/domain"}}},
		},
	}
}

func assertRawCounts(t *testing.T, db *sql.DB, expected map[string]int) {
	t.Helper()
	for table, want := range expected {
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func restartedRealSpecCodingService(t *testing.T, db storecontract.Store, room realSpecCodingRoom) (*app.Service, speccoding.InstalledEnvelope) {
	t.Helper()
	envelope, err := speccoding.LoadInstalledEnvelope(room.repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	return app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
		InstalledSpecCodingEnvelope: &envelope,
	}), envelope
}

func mustRoomRoot(t *testing.T, db storecontract.Store, value realSpecCodingRoom) string {
	t.Helper()
	roomID, err := domain.ParseRoomID(value.id)
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.Reader().GetRoom(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	return room.WorkspaceRoot()
}

func sameCapabilities(left, right domain.CapabilityEnvelope) bool {
	if len(left) != len(right) {
		return false
	}
	for name, allowed := range left {
		if right[name] != allowed {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
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

func submitAndAcceptRealTask(t *testing.T, ctx context.Context, service *app.Service, created app.CreateTaskResult, suffix string) app.ReviewTechnicalPlanRevisionResult {
	t.Helper()
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{
		CommandMeta: meta("submit-real-plan-"+suffix, "submit-real-plan-"+suffix), DraftID: created.Draft.ID(), ExpectedEditVersion: created.Draft.EditVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{
		CommandMeta: meta("accept-real-plan-"+suffix, "accept-real-plan-"+suffix), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Accept exact Plan " + suffix + ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Charter == nil || accepted.Snapshot == nil || accepted.Binding == nil {
		t.Fatalf("accepted %s resources = %#v", suffix, accepted)
	}
	return accepted
}
