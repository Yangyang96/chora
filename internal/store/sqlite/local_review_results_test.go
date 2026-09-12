package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"github.com/Yangyang96/chora/migrations"
)

type localReviewFixture struct {
	db             *sqlite.Store
	seeded         seed
	run            domain.AgentRun
	attempt        domain.Attempt
	report         domain.AgentReport
	result         domain.LocalReviewResult
	patchArtifact  storecontract.Artifact
	declaredDigest [32]byte
}

func TestV28UpgradePreservesPopulatedLocalReviewResult(t *testing.T) {
	ctx := context.Background()
	fixture := newLocalReviewFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate the exact v28 schemas in this test-owned database, retaining its
	// valid Room/Run/Attempt/report/artifact/Result graph, then exercise Open's
	// real forward migration with an existing Result referenced by that graph.
	raw, err := sql.Open("sqlite", fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	defer raw.Close()
	read := func(name string) string {
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	old := read("0028_local_connected_review_results.sql")
	resultSchema := old[:strings.Index(old, "CREATE TABLE local_review_decisions")]
	createEnd := strings.Index(resultSchema, "\n);") + len("\n);")
	create := strings.Replace(resultSchema[:createEnd], "CREATE TABLE local_review_results (", "CREATE TABLE local_review_results_v28 (", 1)
	decisionTrigger := old[strings.Index(old, "CREATE TRIGGER local_review_decisions_authority"):strings.Index(old, "CREATE TRIGGER local_review_decisions_immutable_update")]
	ddl := "BEGIN; DROP TRIGGER local_review_decisions_authority;\n" + create +
		"\nINSERT INTO local_review_results_v28 SELECT * FROM local_review_results;\nDROP TABLE local_review_results;\nALTER TABLE local_review_results_v28 RENAME TO local_review_results;\n" + resultSchema[createEnd:] + decisionTrigger
	for _, item := range []struct{ file, table string }{
		{"0021_patch_applications.sql", "patch_applications"},
		{"0022_patch_inspection_failures.sql", "patch_inspection_failures"},
		{"0016_verified_review_routing.sql", "verified_review_routes"},
	} {
		schema := read(item.file)
		schema = schema[strings.Index(schema, "CREATE TABLE "+item.table):]
		if cut := strings.Index(schema, "-- Accepted real Tasks"); cut >= 0 {
			schema = schema[:cut]
		}
		ddl += "DROP TABLE " + item.table + ";\n" + schema
	}
	// Remove additive v34 tables only in this disposable historical fixture.
	ddl += "DROP TABLE result_closures; DROP TABLE task_delivery_operations; DROP TABLE repository_delivery_defaults; DROP TABLE apply_repository_steps; DROP TABLE apply_operations; DROP TABLE resource_result_reviews; DROP TABLE result_repository_changes; DROP TABLE resource_result_groups; DROP TABLE check_invocations; DROP TABLE repository_check_definitions; DROP TABLE task_repository_worktrees; DROP TABLE task_resource_snapshots; DROP TABLE room_repository_refs; DROP TABLE project_repositories; DROP TABLE repositories; \n"
	// Remove only the post-v29 Task authority additions in this test-owned DB.
	ddl += "DROP TRIGGER task_worktrees_base_authority_insert; DROP TRIGGER task_worktrees_base_authority_update; DROP TRIGGER task_worktrees_immutable_binding; ALTER TABLE task_worktrees DROP COLUMN pinned_base_tree; ALTER TABLE task_worktrees DROP COLUMN base_ref; ALTER TABLE task_worktrees DROP COLUMN start_policy;\n"
	ddl += "DROP TRIGGER project_settings_identity_immutable; DROP TRIGGER project_settings_no_delete; DROP TABLE project_settings; DROP TRIGGER rooms_ownership_insert_valid; DROP TRIGGER rooms_ownership_immutable; DROP TRIGGER projects_default_room_insert_valid; DROP INDEX rooms_project_idx; DROP INDEX projects_state_idx; DROP TABLE project_repository_resources; ALTER TABLE rooms DROP COLUMN ownership_kind; ALTER TABLE rooms DROP COLUMN project_id; DROP TABLE projects;\n"
	worktreeSchema := read("0024_task_worktree_siblings.sql")
	immutable := worktreeSchema[strings.Index(worktreeSchema, "CREATE TRIGGER task_worktrees_immutable_binding"):strings.Index(worktreeSchema, "CREATE TRIGGER task_worktrees_state_transition")]
	ddl += immutable + "ALTER TABLE review_decisions RENAME COLUMN comment TO reviewer_note; DELETE FROM schema_migrations WHERE version>=29; COMMIT;"
	if _, err := raw.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := sqlite.Open(ctx, fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	result, err := upgraded.Reader().GetLocalReviewResultForAgentAttempt(ctx, fixture.attempt.ID())
	if err != nil || result != fixture.result {
		t.Fatalf("v28 Result changed during upgrade: %#v, %v", result, err)
	}
	if version, err := upgraded.SchemaVersion(ctx); err != nil || version != 42 {
		t.Fatalf("version=%d, %v", version, err)
	}
}

func newLocalReviewFixture(t *testing.T) localReviewFixture {
	t.Helper()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	snapshot := assembleSeedSnapshot(t, db, seeded)
	materializedJSON := []byte(`{"schema":"materialized-local-review"}`)
	activeJSON := []byte(`{"schema":"active-local-review"}`)
	binding := storecontract.SpecCodingBinding{
		TaskID: seeded.task.ID(), SnapshotID: snapshot.ID(), SnapshotDigest: snapshot.Digest(),
		MaterializedContractDigest: sha256.Sum256(materializedJSON), MaterializedContractJSON: materializedJSON,
		Status: storecontract.SpecCodingMaterialized, MaterializedAt: seeded.now.Add(time.Second),
	}
	registered := binding
	registered.ActiveContractDigest = sha256.Sum256(activeJSON)
	registered.ActiveContractJSON = activeJSON
	registered.Status = storecontract.SpecCodingRegistered
	registered.RegisteredAt = seeded.now.Add(2 * time.Second)
	profileBinding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}

	attempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1,
		ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "pi",
		AgentExecutionProfileBinding: profileBinding,
		CreatedAt:                    seeded.now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	startingAttempt, err := attempt.Transition(domain.AttemptEventStartAttempt)
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt, err := startingAttempt.Transition(domain.AttemptEventAttemptStarted)
	if err != nil {
		t.Fatal(err)
	}
	outputAttempt, err := runningAttempt.Transition(domain.AttemptEventOutputSubmitted)
	if err != nil {
		t.Fatal(err)
	}
	report, err := domain.NewAgentReport(domain.AgentReportParams{
		ID: domain.NewAgentReportID(), RunID: seeded.run.ID(), AttemptID: attempt.ID(),
		Summary: "Agent reports the Local Connected change is ready for review.", FinalText: "Review the patch.",
		ClaimedChecks: []domain.AgentClaimedCheck{{CriterionID: seeded.task.Criteria()[0].ID(), Status: "passed", Evidence: "agent-reported only"}},
		CompletedAt:   seeded.now.Add(6 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	ready, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	running, err := ready.Transition(domain.CommandStartAttempt, seeded.now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	directReviewAt := seeded.now.Add(7 * time.Second)
	awaitingReview, err := domain.RestoreAgentRun(domain.AgentRunRecord{
		ID: running.ID(), TaskID: running.TaskID(), CharterID: running.CharterID(), State: domain.RunStateAwaitingReview,
		Version: running.Version() + 1, CurrentAttemptNumber: running.CurrentAttemptNumber(), CreatedAt: running.CreatedAt(),
		UpdatedAt: directReviewAt, StartedAt: running.StartedAt(), ReviewRequestedAt: directReviewAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchDigest := sha256.Sum256([]byte("local connected patch"))
	baselineDigest := sha256.Sum256([]byte("local connected baseline"))
	declaredDigest := sha256.Sum256([]byte("declared files"))
	patchArtifact := storecontract.Artifact{
		ID: domain.NewArtifactID(), RunID: seeded.run.ID(), AttemptID: func() *domain.AttemptID { id := attempt.ID(); return &id }(),
		Kind: "patch", Locator: "attempt/local-connected.patch", Digest: &patchDigest, MediaType: "text/x-diff",
		Description: "Agent-reported Local Connected Patch", Role: "output", CreatedAt: seeded.now.Add(7 * time.Second),
	}
	result, err := domain.NewLocalReviewResult(domain.LocalReviewResultParams{
		ID: domain.NewResultID(), RunID: awaitingReview.ID(), AgentAttemptID: outputAttempt.ID(), AgentReportID: report.ID(),
		PatchArtifactID: patchArtifact.ID, PatchDigest: patchDigest, BaselineDigest: baselineDigest,
		ContextSnapshotDigest: snapshot.Digest(), AcceptanceContractDigest: registered.ActiveContractDigest,
		Outcome: domain.ResultReviewReady, CreatedAt: seeded.now.Add(8 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertSpecCodingBinding(ctx, binding); err != nil {
			return fmt.Errorf("insert spec binding: %w", err)
		}
		if err := tx.RegisterSpecCodingBinding(ctx, registered); err != nil {
			return fmt.Errorf("register spec binding: %w", err)
		}
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}
		if err := tx.InsertAgentReport(ctx, report); err != nil {
			return fmt.Errorf("insert AgentReport: %w", err)
		}
		currentRun := seeded.run
		for _, next := range []domain.AgentRun{ready, running, awaitingReview} {
			persisted, err := tx.GetRun(ctx, currentRun.ID())
			if err != nil {
				return fmt.Errorf("load Run before %s: %w", next.State(), err)
			}
			if err := domain.ValidateRunSuccessor(persisted, next); err != nil {
				return fmt.Errorf("validate Run %s: %w", next.State(), err)
			}
			if err := tx.SaveRunCAS(ctx, currentRun.Version(), next); err != nil {
				return fmt.Errorf("save Run %s: %w", next.State(), err)
			}
			currentRun = next
		}
		currentAttempt := attempt
		for _, next := range []domain.Attempt{startingAttempt, runningAttempt, outputAttempt} {
			if err := tx.SaveAttemptCAS(ctx, currentAttempt.State(), next); err != nil {
				return fmt.Errorf("save Attempt %s: %w", next.State(), err)
			}
			currentAttempt = next
		}
		if err := tx.InsertArtifact(ctx, patchArtifact); err != nil {
			return fmt.Errorf("insert Patch artifact: %w", err)
		}
		if err := tx.InsertLocalReviewResult(ctx, result); err != nil {
			return fmt.Errorf("insert LocalReviewResult: %w", err)
		}
		return nil
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return localReviewFixture{
		db: db, seeded: seeded, run: awaitingReview, attempt: outputAttempt, report: report,
		result: result, patchArtifact: patchArtifact, declaredDigest: declaredDigest,
	}
}

func TestLocalReviewResultRoundTripAndWorkspaceProjectionSurviveReopen(t *testing.T) {
	ctx := context.Background()
	fixture := newLocalReviewFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, fixture.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	for _, load := range []func() (domain.LocalReviewResult, error){
		func() (domain.LocalReviewResult, error) {
			return reopened.Reader().GetLocalReviewResult(ctx, fixture.result.ID())
		},
		func() (domain.LocalReviewResult, error) {
			return reopened.Reader().GetLocalReviewResultForAgentAttempt(ctx, fixture.attempt.ID())
		},
	} {
		persisted, err := load()
		if err != nil || persisted.ID() != fixture.result.ID() || persisted.RunID() != fixture.run.ID() ||
			persisted.AgentAttemptID() != fixture.attempt.ID() || persisted.AgentReportID() != fixture.report.ID() ||
			persisted.PatchArtifactID() != fixture.patchArtifact.ID || persisted.PatchDigest() != fixture.result.PatchDigest() ||
			persisted.BaselineDigest() != fixture.result.BaselineDigest() || persisted.ContextSnapshotDigest() != fixture.result.ContextSnapshotDigest() ||
			persisted.AcceptanceContractDigest() != fixture.result.AcceptanceContractDigest() || persisted.Outcome() != domain.ResultReviewReady ||
			!persisted.CreatedAt().Equal(fixture.result.CreatedAt()) {
			t.Fatalf("persisted LocalReviewResult=%#v err=%v", persisted, err)
		}
	}
	workspace, err := reopened.Reader().GetRoomWorkspace(ctx, fixture.seeded.room.ID())
	if err != nil || len(workspace.Tasks) != 1 {
		t.Fatalf("workspace=%#v err=%v", workspace, err)
	}
	item := workspace.Tasks[0]
	if item.LatestRun == nil || item.LatestRun.ID() != fixture.run.ID() || item.LatestRun.State() != domain.RunStateAwaitingReview ||
		item.LatestVerificationResultID != fixture.result.ID() || item.LatestVerificationResultOutcome != domain.ResultReviewReady ||
		item.LatestVerifiedReviewID.Valid() || !item.LastActivityAt.Equal(fixture.result.CreatedAt()) ||
		!workspace.Room.LastActivityAt.Equal(fixture.result.CreatedAt()) {
		t.Fatalf("Local Connected review projection=%#v Room=%#v", item, workspace.Room)
	}
	directory, err := reopened.Reader().ListRoomDirectory(ctx, domain.RoomStateActive)
	if err != nil || len(directory) != 1 || !directory[0].LastActivityAt.Equal(fixture.result.CreatedAt()) {
		t.Fatalf("directory=%#v err=%v", directory, err)
	}
	if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertLocalReviewResult(ctx, fixture.result) }); !errors.Is(err, storecontract.ErrVerificationConflict) {
		t.Fatalf("duplicate LocalReviewResult error=%v", err)
	}
}

func TestLocalReviewDecisionsRoundTripListAndResumeProjection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      domain.ReviewDecisionKind
		class     domain.ReviewRejectionClass
		wantState domain.RunState
	}{
		{name: "accept", kind: domain.ReviewDecisionAccept, wantState: domain.RunStateAccepted},
		{name: "reject ask agent to fix", kind: domain.ReviewDecisionReject, class: domain.ReviewRejectionImplementationGap, wantState: domain.RunStateRevisionRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newLocalReviewFixture(t)
			decidedAt := fixture.result.CreatedAt().Add(time.Second)
			authorization, err := domain.MintHumanReviewAuthorization(fixture.run.ID(), tc.kind, fixture.run.Version(), true)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := domain.NewVerifiedReviewDecision(domain.VerifiedReviewDecisionParams{
				ID: domain.NewReviewDecisionID(), RunID: fixture.run.ID(), ExpectedRunVersion: fixture.run.Version(), Kind: tc.kind,
				Reason: "Device owner reviewed the exact Agent-reported Patch.", RejectionClass: tc.class,
				ActorID: "local-human", SessionID: "local-browser",
				Binding: domain.VerifiedReviewBinding{
					EvidenceKind: domain.ReviewEvidenceAgentReport, ResultID: fixture.result.ID(), AgentAttemptID: fixture.attempt.ID(),
					AgentReportID: fixture.report.ID(), PatchArtifactID: fixture.patchArtifact.ID,
					PatchDigest: fixture.result.PatchDigest(), BaselineDigest: fixture.result.BaselineDigest(), DeclaredFilesDigest: fixture.declaredDigest,
				},
				DecidedAt: decidedAt,
			}, authorization)
			if err != nil {
				t.Fatal(err)
			}
			next, err := fixture.run.ApplyVerifiedReview(decision)
			if err != nil {
				t.Fatal(err)
			}
			key := storecontract.CommandKey{
				Command: "review_verified_result", ResourceID: fixture.run.ID().String(),
				KeyHash: sha256.Sum256([]byte("local-review-key-" + tc.name)), RequestDigest: sha256.Sum256([]byte("local-review-request-" + tc.name)),
				CreatedAt: decidedAt,
			}
			response := storecontract.Response{Status: 200, ContentType: "application/json", Body: []byte(`{"evidence_kind":"agent_reported"}`)}
			if err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				if err := tx.InsertVerifiedReview(ctx, decision); err != nil {
					return err
				}
				if err := tx.SaveRunCAS(ctx, fixture.run.Version(), next); err != nil {
					return err
				}
				return tx.SaveCommand(ctx, key, response)
			}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(ctx, fixture.seeded.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if tc.kind == domain.ReviewDecisionAccept {
				params := domain.PatchApplicationParams{
					RunID: next.ID().String(), ReviewDecisionID: decision.ID().String(),
					PatchDigest: fixture.result.PatchDigest(), DeclaredFilesDigest: fixture.declaredDigest,
					TargetIdentity: "original-checkout", BaseRevision: "admitted-base",
					AffectedPaths: []string{"README.md"}, PreStateDigest: sha256.Sum256([]byte("before")),
					StartedAt: decidedAt, UpdatedAt: decidedAt,
				}
				wrong := params
				wrong.PatchDigest = sha256.Sum256([]byte("unreviewed patch"))
				wrongApplication, err := domain.NewPatchApplication(wrong)
				if err != nil {
					t.Fatal(err)
				}
				if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
					return tx.InsertPatchApplication(ctx, wrongApplication)
				}); !errors.Is(err, storecontract.ErrPatchApplicationConflict) {
					t.Fatalf("unreviewed local Patch accepted: %v", err)
				}
				application, err := domain.NewPatchApplication(params)
				if err != nil {
					t.Fatal(err)
				}
				if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
					return tx.InsertPatchApplication(ctx, application)
				}); err != nil {
					t.Fatalf("accepted local Patch cannot enter Apply: %v", err)
				}
				persisted, err := reopened.Reader().GetPatchApplication(ctx, next.ID())
				if err != nil || persisted.ReviewDecisionID() != decision.ID() || persisted.PatchDigest() != params.PatchDigest {
					t.Fatalf("local Apply binding=%#v, %v", persisted, err)
				}
			}

			for _, load := range []func() (domain.VerifiedReviewDecision, error){
				func() (domain.VerifiedReviewDecision, error) {
					return reopened.Reader().GetVerifiedReview(ctx, decision.ID())
				},
				func() (domain.VerifiedReviewDecision, error) {
					return reopened.Reader().GetVerifiedReviewForResult(ctx, fixture.result.ID())
				},
			} {
				persisted, err := load()
				if err != nil || persisted.ID() != decision.ID() || persisted.RunID() != decision.RunID() ||
					persisted.ExpectedRunVersion() != decision.ExpectedRunVersion() || persisted.Kind() != tc.kind ||
					persisted.RejectionClass() != tc.class || persisted.Binding() != decision.Binding() ||
					persisted.ActorID() != decision.ActorID() || persisted.SessionID() != decision.SessionID() || !persisted.DecidedAt().Equal(decidedAt) {
					t.Fatalf("persisted Local review decision=%#v err=%v", persisted, err)
				}
			}
			history, err := reopened.Reader().ListVerifiedReviewsForRun(ctx, fixture.run.ID())
			if err != nil || len(history) != 1 || history[0].ID() != decision.ID() || history[0].Binding() != decision.Binding() {
				t.Fatalf("review history=%#v err=%v", history, err)
			}
			runs, err := reopened.Reader().ListTaskRunHistory(ctx, fixture.seeded.room.ID(), fixture.seeded.task.ID())
			if err != nil || len(runs) != 1 || runs[0].Run.State() != tc.wantState || runs[0].Run.Version() != next.Version() {
				t.Fatalf("run history=%#v err=%v", runs, err)
			}
			workspace, err := reopened.Reader().GetRoomWorkspace(ctx, fixture.seeded.room.ID())
			if err != nil || len(workspace.Tasks) != 1 {
				t.Fatalf("workspace=%#v err=%v", workspace, err)
			}
			item := workspace.Tasks[0]
			if item.LatestRun == nil || item.LatestRun.State() != tc.wantState || item.LatestVerificationResultID != fixture.result.ID() ||
				item.LatestVerificationResultOutcome != domain.ResultReviewReady || item.LatestVerifiedReviewID != decision.ID() ||
				item.LatestVerifiedReviewKind != tc.kind || item.LatestVerifiedReviewRunVersion != decision.ExpectedRunVersion() ||
				item.LatestRejectionClass != tc.class || !item.LastActivityAt.Equal(decidedAt) || !workspace.Room.LastActivityAt.Equal(decidedAt) {
				t.Fatalf("resumed Local review projection=%#v Room=%#v", item, workspace.Room)
			}
			if tc.kind == domain.ReviewDecisionReject && (item.RouteRejectionClass != "" || item.RoutePlanningDraftID.Valid() || item.RouteRelatedTaskID.Valid()) {
				t.Fatalf("Ask-Agent-to-Fix gained an unrelated successor route: %#v", item)
			}
			var replay storecontract.Response
			var found bool
			if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				var err error
				replay, found, err = tx.LookupCommand(ctx, key)
				return err
			}); err != nil || !found || replay.Status != response.Status || replay.ContentType != response.ContentType || !bytes.Equal(replay.Body, response.Body) {
				t.Fatalf("idempotent replay=%#v found=%v err=%v", replay, found, err)
			}
			conflictingKey := key
			conflictingKey.RequestDigest = sha256.Sum256([]byte("different request"))
			if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				_, _, err := tx.LookupCommand(ctx, conflictingKey)
				return err
			}); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
				t.Fatalf("idempotency conflict error=%v", err)
			}
			if err := reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertVerifiedReview(ctx, decision) }); !errors.Is(err, storecontract.ErrVerificationConflict) {
				t.Fatalf("duplicate Local review decision error=%v", err)
			}
			if tc.kind == domain.ReviewDecisionAccept {
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
				restarted, err := sqlite.Open(ctx, fixture.seeded.path)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				svc := app.NewService(app.Dependencies{Store: restarted, IDs: app.RandomIDs{}, Clock: localReviewClock(decidedAt.Add(time.Second))})
				if err := svc.RecoverStartup(ctx); err != nil {
					t.Fatal(err)
				}
				recovered, err := restarted.Reader().GetPatchApplication(ctx, next.ID())
				if err != nil || recovered.State() != domain.PatchApplicationRecoveryRequired || recovered.PatchDigest() != fixture.result.PatchDigest() {
					t.Fatalf("interrupted Apply recovery=%#v err=%v", recovered, err)
				}
				if recovered.PostStateDigest() != ([32]byte{}) {
					t.Fatal("restart invented applied evidence")
				}
				workspace, err := restarted.Reader().GetRoomWorkspace(ctx, fixture.seeded.room.ID())
				if err != nil || workspace.Tasks[0].LatestPatchApplicationState != domain.PatchApplicationRecoveryRequired {
					t.Fatalf("recovery projection=%#v err=%v", workspace, err)
				}
				if err := svc.RecoverStartup(ctx); err != nil {
					t.Fatal(err)
				}
				again, err := restarted.Reader().GetPatchApplication(ctx, next.ID())
				if err != nil || again.Version() != recovered.Version() {
					t.Fatal("restart recovery was not idempotent")
				}
			}

		})
	}
}

type localReviewClock time.Time

func (clock localReviewClock) Now() time.Time { return time.Time(clock) }
