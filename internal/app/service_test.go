package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

var latestSQLiteTestTemplate struct {
	sync.Once
	data []byte
	err  error
}

func openLatestSQLiteTestStore(ctx context.Context, path string) (*sqlite.Store, error) {
	latestSQLiteTestTemplate.Do(func() {
		root, err := os.MkdirTemp("", "chora-app-sqlite-template-")
		if err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		defer os.RemoveAll(root)
		if err := os.Chmod(root, 0o700); err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		templatePath := root + "/chora.db"
		store, err := sqlite.Open(context.Background(), templatePath)
		if err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		if err := store.Close(); err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		latestSQLiteTestTemplate.data, latestSQLiteTestTemplate.err = os.ReadFile(templatePath)
	})
	if latestSQLiteTestTemplate.err != nil {
		return nil, fmt.Errorf("prepare latest SQLite test template: %w", latestSQLiteTestTemplate.err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create SQLite test directory: %w", err)
	}
	if err := os.WriteFile(path, latestSQLiteTestTemplate.data, 0o600); err != nil {
		return nil, fmt.Errorf("copy latest SQLite test template: %w", err)
	}
	return sqlite.Open(ctx, path)
}

func TestPrepareRunCreatesSnapshotAttemptAndReadyAtomically(t *testing.T) {
	fixture := newFixture(t)
	result, err := fixture.service.PrepareRun(context.Background(), app.PrepareRunRequest{
		CommandMeta: meta("prepare-1", "prepare-run"), RunID: fixture.run.ID(), ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateReady || result.Attempt.Sequence() != 1 || result.Attempt.ContextSnapshotID() != result.Snapshot.ID() || result.Attempt.ContextDigest() != result.Snapshot.Digest() {
		t.Fatalf("invalid prepare result: run=%q attempt=%d", result.Run.State(), result.Attempt.Sequence())
	}
	persisted, err := fixture.db.Reader().GetAttempt(context.Background(), result.Attempt.ID())
	if err != nil || persisted.ContextDigest() != result.Snapshot.Digest() {
		t.Fatalf("persisted attempt = %#v, err=%v", persisted, err)
	}
	events, _ := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if len(events) != 1 || events[0].Type() != "run.prepared" {
		t.Fatalf("events=%v", events)
	}
}

func TestPrepareRunFailsClosedWithoutImmutableTechnicalPlanBinding(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "unbound must fail", "")
	task, err := domain.NewTask(domain.NewTaskID(), fixture.room.ID(), "unbound", "must not execute", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: fixture.charter.ContextRevisionIDs(),
		WorkspaceRoot: fixture.room.WorkspaceRoot(), AdapterID: "fake", SandboxMode: "workspace", ExpectedOutput: "none",
		ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"write": true}, Initiator: "owner", CreatedAt: fixture.run.CreatedAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), fixture.run.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		return tx.InsertRun(ctx, run)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-unbound", "prepare-unbound"), RunID: run.ID(), ExpectedVersion: run.Version()}); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("PrepareRun() error = %v, want ErrInvalidCommand", err)
	}
}

func TestPrepareRunFailsClosedWhenTaskSelectionDiffersFromCharter(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()
	now := fixture.room.CreatedAt().Add(time.Minute)
	other, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: fixture.room.ID(), Kind: domain.ContextKindDecision, RevisionNumber: 1, Title: "different", Body: "different selection", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	otherRecord, err := domain.NewRoomRevision(other, domain.HumanRoomProvenance("owner"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRevision(ctx, other); err != nil {
			return err
		}
		return tx.InsertRoomRevision(ctx, otherRecord)
	}); err != nil {
		t.Fatal(err)
	}
	provenance, err := json.Marshal(otherRecord.Provenance())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP TRIGGER task_revision_selections_immutable_update`); err != nil {
		t.Fatal(err)
	}
	otherDigest := otherRecord.Digest()
	if _, err := raw.Exec(`UPDATE task_revision_selections SET revision_id=?, digest=?, provenance_json=? WHERE task_id=?`, otherRecord.ID().String(), otherDigest[:], provenance, fixture.task.ID().String()); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("selection-mismatch", "selection-mismatch"), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("selection mismatch did not fail closed: %v", err)
	}
	if _, err := fixture.db.Reader().GetCurrentAttempt(ctx, fixture.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("selection mismatch persisted an Attempt: %v", err)
	}
}

func TestStartAttemptCommitsBeforeSideEffectsAndReplaysWithoutRestart(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	fixture.adapter.onPrepare = func() {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err != nil || run.State() != domain.RunStateRunning {
			t.Fatalf("prepare observed run=%q err=%v", run.State(), err)
		}
	}
	request := app.StartAttemptRequest{CommandMeta: meta("start-1", "start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}
	first, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || first.Run.State() != domain.RunStateRunning || fixture.adapter.prepareCalls != 1 || fixture.supervisor.startCalls != 1 {
		t.Fatalf("first=%#v second=%#v prepares=%d starts=%d", first, second, fixture.adapter.prepareCalls, fixture.supervisor.startCalls)
	}
	conflict := request
	conflict.RequestDigest = sha256.Sum256([]byte("different"))
	if replay, err := fixture.service.StartAttempt(context.Background(), conflict); err != nil || !replay.Replayed {
		t.Fatalf("caller digest changed server semantics: replay=%#v err=%v", replay, err)
	}
}

func TestStartInjectsPrivateRuntimeSink(t *testing.T) {
	fixture := newFixture(t)
	fixture.started(t)
	if fixture.supervisor.sink == nil {
		t.Fatal("start received nil runtime sink")
	}
	if !fixture.supervisor.invocation.LaunchToken().Valid() || fixture.supervisor.sink.Binding() != fixture.supervisor.invocation.LaunchToken() || fixture.adapter.startRequest.LaunchToken != fixture.supervisor.invocation.LaunchToken() {
		t.Fatalf("request=%#v sink=%#v invocation=%#v", fixture.adapter.startRequest.LaunchToken, fixture.supervisor.sink.Binding(), fixture.supervisor.invocation.LaunchToken())
	}
}

func TestAgentReportProjectionIsCompleteOrderedAndNonAuthoritative(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	criterionID := fixture.charter.Criteria()[0].ID().String()
	terminal := execution.TerminalResult{
		Kind: execution.TerminalReviewReady, Summary: "complete",
		Outputs:            []execution.WorkspaceArtifact{{Locator: "reports/one.md", Description: "one", SHA256: strings.Repeat("a", 64), MediaType: "text/markdown"}, {Locator: "reports/two.md", Description: "two"}},
		ArtifactCandidates: []execution.WorkspaceArtifact{{Locator: "patches/change.diff", Description: "candidate"}},
		Checks:             []execution.DeclaredCheck{{CriterionID: criterionID, Status: execution.CheckPass, Evidence: "verified"}},
		Unknowns:           []string{"timing unavailable", "remote state unavailable"},
	}
	result := fixture.exit(t, started, "complete-projection", terminal)
	if result.Run.State() != domain.RunStateAwaitingVerification || result.AgentReportID == nil || len(result.Artifacts) != 3 || len(result.Checks) != 0 || len(result.UnknownObservations) != 2 || result.SummaryObservation == nil {
		t.Fatalf("lossy command result: %#v", result)
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projection.Summary.ID.Valid() || len(projection.Artifacts) != 3 || projection.Artifacts[0].Role != "output" || projection.Artifacts[1].Position != 1 || projection.Artifacts[2].Role != "candidate" || len(projection.Checks) != 0 || len(projection.Unknowns) != 0 {
		t.Fatalf("unexpected projection: %#v", projection)
	}
	if projection.Artifacts[0].Digest == nil || fmt.Sprintf("%x", *projection.Artifacts[0].Digest) != strings.Repeat("a", 64) || projection.Artifacts[0].MediaType != "text/markdown" {
		t.Fatalf("artifact identity = %#v", projection.Artifacts[0])
	}
	summary, err := fixture.db.Reader().GetObservation(context.Background(), *result.SummaryObservation)
	if err != nil || summary.Body != "complete" || summary.Role != "agent_report_summary" || summary.AttemptID == nil || *summary.AttemptID != started.Attempt.ID() || summary.SourceEventID == nil {
		t.Fatalf("AgentReport summary=%#v err=%v", summary, err)
	}
	source := *summary.SourceEventID
	for _, artifact := range projection.Artifacts {
		if artifact.RunID != fixture.run.ID() || artifact.AttemptID == nil || *artifact.AttemptID != started.Attempt.ID() || artifact.SourceEventID == nil || *artifact.SourceEventID != source {
			t.Fatalf("artifact binding=%#v", artifact)
		}
	}
	for _, unknownID := range result.UnknownObservations {
		unknown, err := fixture.db.Reader().GetObservation(context.Background(), unknownID)
		if err != nil || unknown.Role != "agent_report_unknown" || unknown.SourceEventID == nil || *unknown.SourceEventID != source {
			t.Fatalf("AgentReport unknown=%#v err=%v", unknown, err)
		}
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil || !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte(result.AgentReportID.String())) || !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte(`"claimed_checks"`)) {
		t.Fatalf("AgentReport event=%s err=%v", events[len(events)-1].NormalizedJSON(), err)
	}
}

func TestAgentReportUsesLatestCompletedAssistantText(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{
		{Type: "assistant_message", NormalizedJSON: []byte(`{"event_type":"assistant_message","text":"first reply"}`)},
		{Type: "assistant_message", NormalizedJSON: []byte(`{"event_type":"assistant_message","text":"真实的最终回复。"}`)},
	}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{
		Chunks:  map[execution.StreamKind][]byte{execution.StreamStdout: []byte("x")},
		Offsets: execution.StreamOffsets{execution.StreamStdout: 1, execution.StreamStderr: 0},
		EOF:     map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
	}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "structured fallback"}
	fixture.signalExit()
	result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("assistant-final-text", "assistant-final-text"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil || result.AgentReportID == nil {
		t.Fatalf("terminal result = %#v, %v", result, err)
	}
	report, err := fixture.db.Reader().GetAgentReportForAttempt(context.Background(), started.Attempt.ID())
	if err != nil || report.Summary() != "structured fallback" || report.FinalText() != "真实的最终回复。" {
		t.Fatalf("AgentReport = summary=%q final=%q err=%v", report.Summary(), report.FinalText(), err)
	}
}

func TestTerminalModelErrorCannotBecomeReviewReady(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "error", NormalizedJSON: []byte(`{"event_type":"error","error_class":"model_error"}`)}}
	result := fixture.exit(t, started, "model-error", execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "must not become reviewable"})
	if result.Run.State() != domain.RunStateRecoveryRequired || result.Attempt.State() != domain.AttemptStateFailed || result.AgentReportID != nil {
		t.Fatalf("model error terminal result = %#v", result)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil || len(events) == 0 || !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte(`"failure_reason":"agent_model_error"`)) {
		t.Fatalf("model error terminal event = %#v, %v", events, err)
	}
}

func TestTerminalProjectionReplayPreservesAllGeneratedIDs(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	criterion := fixture.charter.Criteria()[0].ID().String()
	terminal := execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "done", Outputs: []execution.WorkspaceArtifact{{Locator: "one.txt", Description: "one"}, {Locator: "two.txt", Description: "two"}}, Checks: []execution.DeclaredCheck{{CriterionID: criterion, Status: execution.CheckPass}}, Unknowns: []string{"u1", "u2"}}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminal = terminal
	fixture.signalExit()
	request := app.ExitRequest{CommandMeta: meta("terminal-replay-ids", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}
	first, err := fixture.service.HandleExit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.service.HandleExit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || !reflect.DeepEqual(first.Artifacts, replay.Artifacts) || !reflect.DeepEqual(first.Checks, replay.Checks) || !reflect.DeepEqual(first.UnknownObservations, replay.UnknownObservations) || first.SummaryObservation == nil || replay.SummaryObservation == nil || *first.SummaryObservation != *replay.SummaryObservation {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
}

func TestTerminalProjectionRollsBackOnLateCheckFailure(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	criterion := fixture.charter.Criteria()[0].ID().String()
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "rollback", Outputs: []execution.WorkspaceArtifact{{Locator: "would-leak.txt", Description: "no"}}, Checks: []execution.DeclaredCheck{{CriterionID: criterion, Status: "BOGUS"}}}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("projection-rollback", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err == nil {
		t.Fatal("invalid check status accepted")
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if projection.Summary.ID.Valid() || len(projection.Artifacts) != 0 || len(projection.Checks) != 0 {
		t.Fatalf("partial projection leaked: %#v", projection)
	}
	run, _ := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if run.State() != domain.RunStateRunning {
		t.Fatalf("run=%q", run.State())
	}
}

func TestAgentClaimOutsideFrozenCharterCannotCreateAuthoritativeCheck(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	result := fixture.exit(t, started, "foreign-criterion", execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "malicious", Outputs: []execution.WorkspaceArtifact{{Locator: "out.txt", Description: "must rollback"}}, Checks: []execution.DeclaredCheck{{CriterionID: domain.NewCriterionID().String(), Status: execution.CheckPass}}})
	if result.Run.State() != domain.RunStateAwaitingVerification || result.Attempt.State() != domain.AttemptStateOutputSubmitted || result.AgentReportID == nil || len(result.Checks) != 0 {
		t.Fatalf("run=%q attempt=%q", result.Run.State(), result.Attempt.State())
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Artifacts) != 1 || len(projection.Checks) != 0 || projection.Handoff != nil {
		t.Fatalf("foreign Agent claim created authority: %#v", projection)
	}
}

func TestFirstAttemptRejectsRecordedSessionResume(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	_, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("first-resume", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartResumeRecordedSession})
	if !errors.Is(err, app.ErrInvalidCommand) || fixture.supervisor.startCalls != 0 || fixture.adapter.resumeCalls != 0 {
		t.Fatalf("err=%v starts=%d resumes=%d", err, fixture.supervisor.startCalls, fixture.adapter.resumeCalls)
	}
}

func TestRetryExplicitlyResumesRecordedMatchingSession(t *testing.T) {
	t.Skip("deferred to G2-M5 Agent Retry")
	fixture := newFixture(t)
	started := fixture.started(t)
	session, err := fixture.db.Reader().GetRuntimeSession(context.Background(), started.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessionVersion := session.Version
	session.Version++
	session.ExternalReference = "external-session-1"
	if err := fixture.db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.SaveRuntimeSessionCAS(context.Background(), sessionVersion, session)
	}); err != nil {
		t.Fatal(err)
	}
	revision := fixture.exit(t, started, "resume-revision", execution.TerminalResult{Kind: execution.TerminalRevisionRequired, Summary: "revise"})
	prepared, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("resume-prepare", "same"), RunID: fixture.run.ID(), ExpectedVersion: revision.Run.Version(), Reason: "retry", Instructions: "only change tests"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("resume-start", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartResumeRecordedSession})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRunning || fixture.adapter.resumeCalls != 1 || fixture.adapter.resumeRequest.Binding.ExternalSession != "external-session-1" || string(fixture.adapter.resumeRequest.DeltaInstruction) != "only change tests" || fixture.adapter.resumeRequest.LaunchToken != fixture.supervisor.invocation.LaunchToken() {
		t.Fatalf("result=%#v request=%#v", result, fixture.adapter.resumeRequest)
	}
}

func TestResumeBindingMismatchMatrixNeverStartsOrFallsBackFresh(t *testing.T) {
	t.Skip("deferred to G2-M5 Agent Retry")
	cases := []struct {
		name      string
		configure func(*testing.T, *fixture, storecontract.RuntimeSession)
	}{
		{name: "unsupported capability", configure: func(_ *testing.T, f *fixture, _ storecontract.RuntimeSession) {
			f.adapter.resumeMode = execution.ResumeUnsupported
		}},
		{name: "missing session", configure: func(t *testing.T, f *fixture, s storecontract.RuntimeSession) {
			updateRuntimeBindingRaw(t, f.dbPath, s.ID, "external_reference", "")
		}},
		{name: "session mismatch", configure: func(t *testing.T, f *fixture, _ storecontract.RuntimeSession) {
			updateAttemptExternalRaw(t, f.dbPath, "external-session-other")
		}},
		{name: "digest mismatch", configure: func(_ *testing.T, f *fixture, _ storecontract.RuntimeSession) {
			f.adapter.fingerprint = execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte("different-runtime")), Version: "test-v1"}
		}},
		{name: "version mismatch", configure: func(_ *testing.T, f *fixture, _ storecontract.RuntimeSession) {
			f.adapter.fingerprint = execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte("runtime")), Version: "test-v2"}
		}},
		{name: "root mismatch", configure: func(t *testing.T, f *fixture, s storecontract.RuntimeSession) {
			updateRuntimeBindingRaw(t, f.dbPath, s.ID, "working_root", f.room.WorkspaceRoot()+"-other")
		}},
		{name: "security mismatch", configure: func(t *testing.T, f *fixture, s storecontract.RuntimeSession) {
			fingerprint := sha256.Sum256([]byte("different-security"))
			updateRuntimeBindingRaw(t, f.dbPath, s.ID, "security_fingerprint", fingerprint[:])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t)
			started := fixture.started(t)
			session, err := fixture.db.Reader().GetRuntimeSession(context.Background(), started.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			version := session.Version
			session.Version++
			session.ExternalReference = "external-session-1"
			if err := fixture.db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
				return tx.SaveRuntimeSessionCAS(context.Background(), version, session)
			}); err != nil {
				t.Fatal(err)
			}
			revision := fixture.exit(t, started, "resume-matrix-revision", execution.TerminalResult{Kind: execution.TerminalRevisionRequired, Summary: "revise"})
			prepared, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("resume-matrix-prepare", "same"), RunID: fixture.run.ID(), ExpectedVersion: revision.Run.Version(), Reason: "retry", Instructions: "delta"})
			if err != nil {
				t.Fatal(err)
			}
			tc.configure(t, fixture, session)
			starts := fixture.supervisor.startCalls
			fresh := fixture.adapter.prepareCalls
			result, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("resume-matrix-start", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartResumeRecordedSession})
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.State() != domain.RunStateRecoveryRequired || fixture.supervisor.startCalls != starts || fixture.adapter.prepareCalls != fresh || fixture.adapter.resumeCalls != 0 {
				t.Fatalf("result=%#v starts=%d/%d fresh=%d/%d resumes=%d", result, fixture.supervisor.startCalls, starts, fixture.adapter.prepareCalls, fresh, fixture.adapter.resumeCalls)
			}
		})
	}
}

func TestDeterministicFakeScenariosDriveCompleteSQLiteFlow(t *testing.T) {
	cases := []struct {
		name          string
		want          domain.RunState
		wantArtifacts int
		wantHandoff   bool
	}{
		{name: agentfake.ScenarioSuccess, want: domain.RunStateAwaitingVerification, wantArtifacts: 1},
		{name: agentfake.ScenarioNeedsRevision, want: domain.RunStateAwaitingVerification},
		{name: agentfake.ScenarioHandoff, want: domain.RunStateAwaitingVerification, wantHandoff: true},
		{name: agentfake.ScenarioMalformedResult, want: domain.RunStateRecoveryRequired},
		{name: agentfake.ScenarioNonzeroExit, want: domain.RunStateRecoveryRequired},
		{name: agentfake.ScenarioSlowCancellation, want: domain.RunStateCancelled},
		{name: agentfake.ScenarioUncertainStop, want: domain.RunStateRecoveryRequired},
		{name: agentfake.ScenarioRestartRecovery, want: domain.RunStateRecoveryRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t)
			script, err := agentfake.NewScript(tc.name, fixture.charter.Criteria()[0].ID().String())
			if err != nil {
				t.Fatal(err)
			}
			adapter := agentfake.NewAdapter(script.Plan)
			fixture.supervisor.outcome = script.Supervisor.StartOutcome
			for _, fact := range script.Supervisor.ReadChunks {
				fixture.supervisor.readChunks = append(fixture.supervisor.readChunks, fact.Chunk)
			}
			fixture.supervisor.drainOutcomes = append([]execution.DrainOutcome(nil), script.Supervisor.DrainOutcomes...)
			fixture.supervisor.stopOutcome = script.Supervisor.StopOutcome
			fixture.supervisor.reconcileOutcomes = append([]execution.ReconcileOutcome(nil), script.Supervisor.ReconcileOutcomes...)
			fixture.service = app.NewService(app.Dependencies{Store: fixture.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter: adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: fixture.charter.CreatedAt()}, IDs: app.RandomIDs{}})
			started := fixture.started(t)
			for index, fact := range script.Supervisor.ReadChunks {
				fixture.supervisor.sink.Notify(fact.Stream, fact.Chunk.NextOffset)
				if err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta(fmt.Sprintf("scenario-%s-stream-%d", tc.name, index), "same"), SessionID: started.Session.ID, Stream: fact.Stream}); err != nil {
					t.Fatal(err)
				}
			}
			events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, event := range events {
				seen[event.Type()] = true
			}
			if !seen["thread.started"] || !seen["runtime.notice"] {
				t.Fatalf("transcript not consumed: %v", seen)
			}
			var run domain.AgentRun
			var artifactCount int
			var handoff bool
			switch tc.name {
			case agentfake.ScenarioSuccess, agentfake.ScenarioNeedsRevision, agentfake.ScenarioHandoff, agentfake.ScenarioMalformedResult, agentfake.ScenarioNonzeroExit:
				fixture.signalExit()
				result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("scenario-"+tc.name, "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
				if err != nil {
					t.Fatal(err)
				}
				run, artifactCount, handoff = result.Run, len(result.Artifacts), result.Handoff != nil
			case agentfake.ScenarioSlowCancellation:
				block := make(chan struct{})
				fixture.supervisor.stopBlock = block
				done, errs := make(chan app.StopResult, 1), make(chan error, 1)
				go func() {
					result, err := fixture.service.RequestCancel(context.Background(), app.StopRequest{CommandMeta: meta("scenario-slow-cancel", "same"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "cancel"})
					done <- result
					errs <- err
				}()
				waitForRunState(t, fixture, domain.RunStateStopping)
				close(block)
				result := <-done
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				run = result.Run
			case agentfake.ScenarioUncertainStop:
				result, err := fixture.service.RequestIntervention(context.Background(), app.StopRequest{CommandMeta: meta("scenario-uncertain", "same"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "revise"})
				if err != nil {
					t.Fatal(err)
				}
				run = result.Run
			case agentfake.ScenarioRestartRecovery:
				if err := fixture.service.RecoverStartup(context.Background()); err != nil {
					t.Fatal(err)
				}
				run, err = fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
				if err != nil {
					t.Fatal(err)
				}
			}
			if run.State() != tc.want || artifactCount != tc.wantArtifacts || handoff != tc.wantHandoff {
				t.Fatalf("run=%q artifacts=%d handoff=%t", run.State(), artifactCount, handoff)
			}
			for _, fact := range script.Supervisor.ReadChunks {
				stream := storecontract.RuntimeStreamStdout
				if fact.Stream == execution.StreamStderr {
					stream = storecontract.RuntimeStreamStderr
				}
				offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, stream)
				if err != nil || offset.Offset != fact.Chunk.NextOffset {
					t.Fatalf("stream=%q offset=%#v err=%v", fact.Stream, offset, err)
				}
			}
		})
	}
}

func waitForRunState(t *testing.T, fixture *fixture, state domain.RunState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err == nil && run.State() == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run never reached %q", state)
}

func TestStartReplayMatchesOriginalPublicResultAndExposesNoHandle(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	request := app.StartAttemptRequest{CommandMeta: meta("start-public-replay", "same-caller-digest"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}
	first, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay.Replayed = false
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
	if _, exposed := reflect.TypeOf(app.RuntimeSessionResult{}).FieldByName("Handle"); exposed {
		t.Fatal("public runtime session result exposes supervisor handle")
	}
}

func TestCreateRoomSemanticDigestRejectsChangedFields(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(ctx, dbDir+"/semantic.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	request := app.CreateRoomRequest{CommandMeta: meta("same-key", "same-caller-digest"), Name: "alpha", Description: "desc", WorkspaceRoot: t.TempDir()}
	if _, err := service.CreateRoom(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Name = "beta"
	if _, err := service.CreateRoom(ctx, request); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("changed semantic fields replayed: %v", err)
	}
}

func TestConcurrentStartOnlyStartsOnce(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	request := app.StartAttemptRequest{CommandMeta: meta("start-concurrent", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fixture.service.StartAttempt(context.Background(), request)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.supervisor.startCalls != 1 {
		t.Fatalf("start calls=%d", fixture.supervisor.startCalls)
	}
}

func TestStartOutcomeReconciliationRequiredKeepsAttemptStarting(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	fixture.supervisor.outcome = execution.StartOutcome{Kind: execution.StartReconciliationRequired, Identity: execution.ProcessIdentity{Value: "pid:42"}, Diagnostic: "handshake persistence uncertain"}
	result, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("start-uncertain", "start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired || result.Attempt.State() != domain.AttemptStateStarting {
		t.Fatalf("run=%q attempt=%q", result.Run.State(), result.Attempt.State())
	}
}

func TestLaunchTokenOnlyRecoveryReconcilesAndBlocksRetryWhileAlive(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	fixture.supervisor.outcome = execution.StartOutcome{Kind: execution.StartReconciliationRequired, Diagnostic: "identity handshake missing"}
	started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("launch-only", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if session.ProcessIdentity != "" || session.LaunchToken == "" || session.LaunchToken != fixture.supervisor.invocation.LaunchToken().Value || fixture.supervisor.sink.Binding().Value != session.LaunchToken {
		t.Fatalf("session=%#v invocation=%#v sink=%#v", session, fixture.supervisor.invocation.LaunchToken(), fixture.supervisor.sink.Binding())
	}
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: execution.LaunchToken{Value: session.LaunchToken}}
	if err := fixture.service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.reconcileLaunchCalls != 1 {
		t.Fatalf("launch reconciles=%d", fixture.supervisor.reconcileLaunchCalls)
	}
	if _, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("retry-live-launch", "same"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "retry", Instructions: "again"}); err == nil {
		t.Fatal("retry accepted before launch token was proven dead")
	}
	if fixture.supervisor.startCalls != 1 {
		t.Fatalf("children started=%d", fixture.supervisor.startCalls)
	}
}

func TestReviewAcceptClosesTaskAtomicallyAndConcurrentVersionLoses(t *testing.T) {
	t.Skip("deferred to G2-M5 human Accept")
	fixture := newFixture(t)
	awaiting := fixture.awaitingReview(t)
	request := app.ReviewRequest{CommandMeta: meta("accept-1", "accept"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version(), Kind: domain.ReviewDecisionAccept, Comment: "accepted"}
	accepted, err := fixture.service.Review(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Run.State() != domain.RunStateAccepted {
		t.Fatalf("state=%q", accepted.Run.State())
	}
	task, err := fixture.db.Reader().GetTask(context.Background(), fixture.task.ID())
	if err != nil || task.State() != domain.TaskStateClosed {
		t.Fatalf("task=%q err=%v", task.State(), err)
	}
	loser := request
	loser.IdempotencyKey = "accept-2"
	loser.RequestDigest = sha256.Sum256([]byte("accept-2"))
	if _, err := fixture.service.Review(context.Background(), loser); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("loser err=%v", err)
	}
}

func TestPrepareRetryCreatesSuccessorSnapshotAndAttempt(t *testing.T) {
	t.Skip("deferred to G2-M5 Agent Retry")
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("retry-start", "start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	revision := fixture.exit(t, started, "retry-output", execution.TerminalResult{Kind: execution.TerminalRevisionRequired})
	retry, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("retry-prepare", "retry"), RunID: fixture.run.ID(), ExpectedVersion: revision.Run.Version(), Reason: "feedback", Instructions: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	pred, ok := retry.Attempt.Predecessor()
	if !ok || pred != prepared.Attempt.ID() || retry.Attempt.Sequence() != 2 || retry.Attempt.ContextDigest() == prepared.Attempt.ContextDigest() {
		t.Fatalf("retry=%#v predecessor=%v,%v", retry.Attempt, pred, ok)
	}
}

func TestPrepareRetryRequiresPersistedCancelledRetryAuthority(t *testing.T) {
	for _, test := range []struct {
		name         string
		allowRetry   bool
		wantRejected bool
	}{
		{name: "explicit user cancellation", allowRetry: true},
		{name: "policy cancellation", wantRejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			started := fixture.started(t)
			cancelled, err := fixture.service.RequestCancel(context.Background(), app.StopRequest{
				CommandMeta: meta("cancel-retry", test.name), RunID: fixture.run.ID(),
				ExpectedVersion: started.Run.Version(), Reason: test.name, AllowRetry: test.allowRetry,
			})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
				CommandMeta: meta("prepare-cancel-retry", test.name), RunID: fixture.run.ID(),
				ExpectedVersion: cancelled.Run.Version(), Reason: test.name, Instructions: "fresh attempt",
			})
			if test.wantRejected {
				if !errors.Is(err, app.ErrInvalidCommand) {
					t.Fatalf("PrepareRetry() error = %v, want ErrInvalidCommand", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if retry.Run.State() != domain.RunStateReady || retry.Attempt.Sequence() != 2 {
				t.Fatalf("retry = %#v", retry)
			}
		})
	}
}

func TestStartCommitFailureHasNoExternalSideEffects(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	failing := &failingStore{Store: fixture.db, err: storecontract.ErrCommitUnknown}
	service := app.NewService(app.Dependencies{Store: failing, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	_, err := service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("failed-start", "failed-start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if !errors.Is(err, storecontract.ErrCommitUnknown) {
		t.Fatalf("err=%v", err)
	}
	if fixture.adapter.prepareCalls != 0 || fixture.supervisor.startCalls != 0 {
		t.Fatalf("prepare=%d start=%d", fixture.adapter.prepareCalls, fixture.supervisor.startCalls)
	}
}

func TestRequestHandoffIsStoppingWhileStopBlocksThenMapsUncertain(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	block := make(chan struct{})
	fixture.supervisor.stopBlock = block
	fixture.supervisor.stopOutcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "cannot prove death"}
	done := make(chan app.StopResult, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := fixture.service.RequestHandoff(context.Background(), app.StopRequest{CommandMeta: meta("handoff", "handoff"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), ToActor: "owner", Reason: "needs human"})
		done <- result
		errs <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err == nil && run.State() == domain.RunStateStopping {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never entered stopping")
		}
		time.Sleep(time.Millisecond)
	}
	close(block)
	result := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired || result.HandoffID == nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestStartupRecoveryMarksCandidateBeforeReconcileAndNeverStarts(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.supervisor.reconcileCheck = func() {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err != nil || run.State() != domain.RunStateRecoveryRequired {
			t.Fatalf("reconcile observed %q err=%v", run.State(), err)
		}
	}
	if err := fixture.service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.reconcileCalls != 1 || fixture.supervisor.startCalls != 1 {
		t.Fatalf("reconcile=%d start=%d", fixture.supervisor.reconcileCalls, fixture.supervisor.startCalls)
	}
	attempt, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State() != domain.AttemptStateInterrupted {
		t.Fatalf("attempt=%q", attempt.State())
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || !offset.EOF {
		t.Fatalf("offset=%#v err=%v", offset, err)
	}
}

func TestConsumeStreamReadsFromPersistedOffset(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"message":"one"}`), NextOffset: 17}, {Data: []byte(`{"message":"two"}`), NextOffset: 34}}
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.message", OccurredAt: time.Now().UTC(), NormalizedJSON: []byte(`{"message":"normalized"}`)}}
	request := app.ConsumeStreamRequest{CommandMeta: meta("stream", "stream"), SessionID: started.Session.ID, Stream: execution.StreamStdout}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 17)
	if err := fixture.service.ConsumeStream(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 34)
	if err := fixture.service.ConsumeStream(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fixture.supervisor.readOffsets) != 2 || fixture.supervisor.readOffsets[0] != 0 || fixture.supervisor.readOffsets[1] != 17 {
		t.Fatalf("offsets=%v", fixture.supervisor.readOffsets)
	}
	if fixture.supervisor.readLimits[0] != 10*1024*1024 || fixture.supervisor.readLimits[1] != 10*1024*1024 {
		t.Fatalf("limits=%v", fixture.supervisor.readLimits)
	}
}

func TestConsumeStreamClosesHeldStdinAfterPersistingAgentSettled(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent_settled", NormalizedJSON: []byte(`{"event_type":"agent_settled"}`)}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"type":"agent_settled"}`), NextOffset: 24}}
	persistedBeforeClose := false
	fixture.supervisor.closeStdinCheck = func() {
		offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
		if err != nil || offset.Offset != 24 {
			t.Errorf("offset before stdin close = %#v, %v", offset, err)
			return
		}
		events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
		if err != nil || len(events) == 0 || events[len(events)-1].Type() != "agent_settled" {
			t.Errorf("events before stdin close = %#v, %v", events, err)
			return
		}
		persistedBeforeClose = true
	}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 24)
	if err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("agent-settled", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.closeStdinCalls != 1 || !persistedBeforeClose {
		t.Fatalf("close stdin calls=%d persistedBeforeClose=%v", fixture.supervisor.closeStdinCalls, persistedBeforeClose)
	}
}

func TestConsumeStreamRejectsCallsWithoutSupervisorNotification(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("stream-without-notify", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("unproven stream consumption err=%v", err)
	}
}

func TestConsumeStreamCommitsPolicyViolationBeforeFailingClosed(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "error", NormalizedJSON: []byte(`{"event_type":"error","error_class":"undeclared_path"}`)}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"unsafe":true}`), NextOffset: 15}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 15)
	err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("policy-violation", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout})
	if !errors.Is(err, app.ErrRuntimePolicyViolation) {
		t.Fatalf("policy violation error = %v", err)
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 15 {
		t.Fatalf("policy violation offset = %#v, %v", offset, err)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil || events[len(events)-1].Type() != "error" || !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte(`"error_class":"undeclared_path"`)) {
		t.Fatalf("policy violation event = %#v, %v", events, err)
	}
}

func TestAdapterDiagnosticBytesAreNeverPersistedInRunEvents(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	secret := []byte(`{"token":"secret-decoy","prompt":"private"}`)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.safe", OccurredAt: time.Now().UTC(), NormalizedJSON: []byte(`{"status":"redacted"}`), DiagnosticJSON: secret}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"wire":true}`), NextOffset: 13}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 13)
	if err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("redaction", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err != nil {
		t.Fatal(err)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	event := events[len(events)-1]
	if len(event.RawJSON()) != 0 || bytes.Contains(event.NormalizedJSON(), []byte("secret-decoy")) {
		t.Fatalf("raw=%s normalized=%s", event.RawJSON(), event.NormalizedJSON())
	}
}

func TestConsumeStreamCommitsOnlyConsumedBytesAndPersistsSafeRawSessionBinding(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.consumed = 5
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.session", NormalizedJSON: []byte(`{"safe":true}`), RawJSON: []byte(`{"redacted":"safe"}`), ExternalSession: "session-explicit-1"}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte("line\npartial"), NextOffset: 12}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 12)
	if err := fixture.service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("partial-stream", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err != nil {
		t.Fatal(err)
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 5 {
		t.Fatalf("offset=%#v err=%v", offset, err)
	}
	session, err := fixture.db.Reader().GetRuntimeSession(context.Background(), started.Session.ID)
	if err != nil || session.ExternalReference != "session-explicit-1" {
		t.Fatalf("session=%#v err=%v", session, err)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil || string(events[len(events)-1].RawJSON()) != `{"redacted":"safe"}` {
		t.Fatalf("event=%#v err=%v", events[len(events)-1], err)
	}
}

func TestConsumeStreamRejectsExternalSessionRebindingWithoutAdvancingOffset(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.session", ExternalSession: "session-one"}}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte("first"), NextOffset: 5}, {Data: []byte("second"), NextOffset: 11}}
	request := app.ConsumeStreamRequest{CommandMeta: meta("session-one", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 5)
	if err := fixture.service.ConsumeStream(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.session", ExternalSession: "session-two"}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 11)
	if err := fixture.service.ConsumeStream(context.Background(), request); err == nil {
		t.Fatal("conflicting session reference accepted")
	}
	offset, _ := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if offset.Offset != 5 {
		t.Fatalf("offset advanced on conflict: %#v", offset)
	}
}

func TestHandleExitRejectsAliveRuntimeEvenAfterExitSignal(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle"}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.supervisor.sink.Exited()
	_, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("alive-exit", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("alive exit accepted: %v", err)
	}
	if fixture.supervisor.finalizeCalls != 0 {
		t.Fatalf("alive runtime finalized %d times", fixture.supervisor.finalizeCalls)
	}
}

func TestHandleExitRejectsDeadReconcileWithoutTrustedExitSignal(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	_, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("forged-exit", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("untrusted exit accepted: %v", err)
	}
	if fixture.supervisor.finalizeCalls != 0 {
		t.Fatalf("untrusted exit finalized %d times", fixture.supervisor.finalizeCalls)
	}
}

func TestExitDrainsToEOFBeforeFinalize(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte(`{"done":true}`)}, Offsets: execution.StreamOffsets{execution.StreamStdout: 13, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.done", OccurredAt: time.Now().UTC(), NormalizedJSON: []byte(`{"done":true}`)}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("exit", "exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.finalizeCalls != 1 {
		t.Fatalf("finalize=%d", fixture.supervisor.finalizeCalls)
	}
}

func TestExitDrainsIncrementallyWithBoundedChunks(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.decode = []execution.NormalizedEvent{{Type: "agent.chunk", OccurredAt: time.Now().UTC(), NormalizedJSON: []byte(`{"safe":true}`)}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.supervisor.drainOutcomes = []execution.DrainOutcome{
		{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte("first")}, Offsets: execution.StreamOffsets{execution.StreamStdout: 5, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: false}},
		{Chunks: map[execution.StreamKind][]byte{execution.StreamStderr: []byte("err")}, Offsets: execution.StreamOffsets{execution.StreamStdout: 5, execution.StreamStderr: 3}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}},
	}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("incremental-exit", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.supervisor.drainLimits) != 2 || fixture.supervisor.drainLimits[0] != 10*1024*1024 || fixture.supervisor.drainLimits[1] != 10*1024*1024 {
		t.Fatalf("limits=%v", fixture.supervisor.drainLimits)
	}
	stdoutOffset, stdoutPresent := fixture.supervisor.drainOffsets[1][execution.StreamStdout]
	if len(fixture.supervisor.drainOffsets) != 2 || !stdoutPresent || stdoutOffset != 5 {
		t.Fatalf("offsets=%v", fixture.supervisor.drainOffsets)
	}
}

func TestExitDrainsOneBoundedEventLargerThan64KiB(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	largeEvent := make([]byte, 70*1024)
	largeEvent[len(largeEvent)-1] = '\n'
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{
		Chunks:  map[execution.StreamKind][]byte{execution.StreamStdout: largeEvent},
		Offsets: execution.StreamOffsets{execution.StreamStdout: int64(len(largeEvent)), execution.StreamStderr: 0},
		EOF:     map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
	}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("large-event-exit", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.finalizeCalls != 1 {
		t.Fatalf("finalize=%d", fixture.supervisor.finalizeCalls)
	}
}

func TestExitRejectsOversizedDrainChunk(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.supervisor.drainOutcomes = []execution.DrainOutcome{{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: make([]byte, 10*1024*1024+1)}, Offsets: execution.StreamOffsets{execution.StreamStdout: 10*1024*1024 + 1, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("oversized-exit", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err == nil {
		t.Fatal("oversized drain chunk accepted")
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 0 {
		t.Fatalf("offset=%#v err=%v", offset, err)
	}
}

func TestExitDrainFailureRetriesFromCommittedChunkOffset(t *testing.T) {
	fixture := newFixture(t)
	store := &toggleFailStore{Store: fixture.db, err: errors.New("chunk commit failed")}
	service := app.NewService(app.Dependencies{Store: store, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	prepared, err := service.PrepareRun(context.Background(), app.PrepareRunRequest{CommandMeta: meta("drain-prepare", "same"), RunID: fixture.run.ID(), ExpectedVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("drain-start", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	first := execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte("first")}, Offsets: execution.StreamOffsets{execution.StreamStdout: 5, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: false, execution.StreamStderr: false}}
	last := execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte("second")}, Offsets: execution.StreamOffsets{execution.StreamStdout: 11, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.supervisor.drainOutcomes = []execution.DrainOutcome{first, last, last}
	fixture.supervisor.onDrain = func(call int) {
		if call == 2 {
			store.setFail(true)
		}
	}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalRevisionRequired}
	fixture.signalExit()
	request := app.ExitRequest{CommandMeta: meta("drain-retry", "same"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}
	if _, err := service.HandleExit(context.Background(), request); err == nil {
		t.Fatal("expected second chunk commit failure")
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 5 {
		t.Fatalf("offset=%#v err=%v", offset, err)
	}
	store.setFail(false)
	if _, err := service.HandleExit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fixture.supervisor.drainOffsets) < 3 || fixture.supervisor.drainOffsets[2][execution.StreamStdout] != 5 {
		t.Fatalf("drain offsets=%v", fixture.supervisor.drainOffsets)
	}
}

func TestStartProvenNoChildFailsAttemptAndRequiresRecovery(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	fixture.supervisor.outcome = execution.StartOutcome{Kind: execution.StartProvenNoChild, Diagnostic: "exec failed before child"}
	result, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("no-child", "no-child"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired || result.Attempt.State() != domain.AttemptStateFailed {
		t.Fatalf("run=%q attempt=%q", result.Run.State(), result.Attempt.State())
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), result.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Unknowns) != 1 || projection.Unknowns[0].Body != "The managed Agent did not start; no child process was created." {
		t.Fatalf("start failure unknowns = %#v", projection.Unknowns)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), result.Attempt.ID())
	if err != nil || session.State != "failed" || session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() || session.ProcessIdentity != "" {
		t.Fatalf("proven-no-child session = %#v, %v", session, err)
	}
	retry, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
		CommandMeta: meta("retry-no-child", "retry-no-child"), RunID: result.Run.ID(), ExpectedVersion: result.Run.Version(),
		Reason: "Agent launch failed before a child existed.", Instructions: "Retry with the unchanged frozen inputs.",
	})
	if err != nil || retry.Run.State() != domain.RunStateReady || retry.Attempt.Sequence() != 2 {
		t.Fatalf("proven-no-child retry = %#v, %v", retry, err)
	}
}

func TestStartAttemptUsesOptionalProfileBindingFingerprintBeforeChildStart(t *testing.T) {
	t.Run("persists exact binding fingerprint", func(t *testing.T) {
		fixture := newFixture(t)
		want := execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte("profile-source-fingerprint")), Version: "pi-profile-v1"}
		adapter := &bindingFingerprintFakeAdapter{fakeAdapter: fixture.adapter, bindingFingerprint: want}
		fixture.service = app.NewService(app.Dependencies{
			Store: fixture.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter}, Supervisor: fixture.supervisor,
			Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: fixture.run.CreatedAt()}, IDs: app.RandomIDs{},
		})
		prepared := fixture.prepare(t)
		started, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{
			CommandMeta: meta("binding-fingerprint", "binding-fingerprint"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
		})
		if err != nil {
			t.Fatal(err)
		}
		session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
		if err != nil || adapter.bindingCalls != 1 || session.RuntimeFingerprint != want.Digest || session.RuntimeVersion != want.Version {
			t.Fatalf("calls=%d binding=%#v session=%#v err=%v", adapter.bindingCalls, adapter.binding, session, err)
		}
	})

	t.Run("fingerprint failure proves no child", func(t *testing.T) {
		fixture := newFixture(t)
		adapter := &bindingFingerprintFakeAdapter{fakeAdapter: fixture.adapter, bindingErr: errors.New("Trusted Local source is not configured")}
		fixture.service = app.NewService(app.Dependencies{
			Store: fixture.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter}, Supervisor: fixture.supervisor,
			Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: fixture.run.CreatedAt()}, IDs: app.RandomIDs{},
		})
		prepared := fixture.prepare(t)
		result, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{
			CommandMeta: meta("binding-fingerprint-failure", "binding-fingerprint-failure"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh,
		})
		if err != nil {
			t.Fatal(err)
		}
		if adapter.bindingCalls != 1 || fixture.supervisor.startCalls != 0 || result.Attempt.State() != domain.AttemptStateFailed || result.Run.State() != domain.RunStateRecoveryRequired {
			t.Fatalf("calls=%d starts=%d result=%#v", adapter.bindingCalls, fixture.supervisor.startCalls, result)
		}
	})
}

func TestTerminalNeedsRevisionCreatesOutputAndHandoffAtomically(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	result := fixture.exit(t, started, "terminal-handoff", execution.TerminalResult{Kind: execution.TerminalHandoffRequested, Summary: "needs a decision", Outputs: []execution.WorkspaceArtifact{{Locator: "patch.diff", Description: "patch"}}, Handoff: &execution.HandoffRequest{Reason: "needs decision"}})
	if result.Run.State() != domain.RunStateAwaitingVerification || len(result.Artifacts) != 1 || result.Handoff == nil {
		t.Fatalf("result=%#v", result)
	}
	if _, err := fixture.db.Reader().GetHandoff(context.Background(), *result.Handoff); err != nil {
		t.Fatal(err)
	}
}

func TestReviewRequiresPresenceAndCloseFailureRollsBack(t *testing.T) {
	t.Skip("deferred to G2-M5 human Accept")
	fixture := newFixture(t)
	awaiting := fixture.awaitingReview(t)
	unauthorized := app.NewService(app.Dependencies{Store: fixture.db, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	request := app.ReviewRequest{CommandMeta: meta("unauthorized", "unauthorized"), RunID: fixture.run.ID(), ExpectedVersion: awaiting.Version(), Kind: domain.ReviewDecisionAccept}
	if _, err := unauthorized.Review(context.Background(), request); !errors.Is(err, domain.ErrUnauthorizedReview) {
		t.Fatalf("err=%v", err)
	}
	failing := &closeFailStore{Store: fixture.db}
	service := app.NewService(app.Dependencies{Store: failing, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	request.IdempotencyKey = "close-fail"
	request.RequestDigest = sha256.Sum256([]byte("close-fail"))
	if _, err := service.Review(context.Background(), request); !errors.Is(err, errCloseTask) {
		t.Fatalf("err=%v", err)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if run.State() != domain.RunStateAwaitingVerification {
		t.Fatalf("state=%q", run.State())
	}
}

func TestStreamCommitFailureRetainsNotificationProofForRetry(t *testing.T) {
	fixture := newFixture(t)
	store := &toggleFailStore{Store: fixture.db, err: errors.New("commit failed")}
	service := app.NewService(app.Dependencies{Store: store, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	prepared, err := service.PrepareRun(context.Background(), app.PrepareRunRequest{CommandMeta: meta("proof-prepare", "same"), RunID: fixture.run.ID(), ExpectedVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("proof-start", "same"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"x":1}`), NextOffset: 7}}
	fixture.supervisor.sink.Notify(execution.StreamStdout, 7)
	store.setFail(true)
	if err := service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("stream-fail", "stream-fail"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err == nil {
		t.Fatal("expected commit failure")
	}
	offset, err := fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 0 {
		t.Fatalf("offset=%d err=%v", offset.Offset, err)
	}
	store.setFail(false)
	fixture.supervisor.readChunks = []execution.StreamChunk{{Data: []byte(`{"x":1}`), NextOffset: 7}}
	if err := service.ConsumeStream(context.Background(), app.ConsumeStreamRequest{CommandMeta: meta("stream-retry", "same"), SessionID: started.Session.ID, Stream: execution.StreamStdout}); err != nil {
		t.Fatal(err)
	}
	offset, err = fixture.db.Reader().GetRuntimeStreamOffset(context.Background(), started.Session.ID, storecontract.RuntimeStreamStdout)
	if err != nil || offset.Offset != 7 {
		t.Fatalf("offset=%d err=%v", offset.Offset, err)
	}
}

func TestPrepareReplayReturnsOriginalReadyResultAfterRunAdvances(t *testing.T) {
	fixture := newFixture(t)
	request := app.PrepareRunRequest{CommandMeta: meta("original-prepare", "original-prepare"), RunID: fixture.run.ID(), ExpectedVersion: 0}
	prepared, err := fixture.service.PrepareRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("advance-start", "advance-start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}); err != nil {
		t.Fatal(err)
	}
	replayed, err := fixture.service.PrepareRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Run.State() != domain.RunStateReady || replayed.Run.Version() != prepared.Run.Version() || replayed.Attempt.State() != domain.AttemptStateCreated {
		t.Fatalf("replay drifted: %#v", replayed)
	}
}

func TestHandleExitFinalizesBeforeDecodedTerminalCommit(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady}
	fixture.supervisor.finalizeCheck = func() {
		run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
		if err != nil || run.State() != domain.RunStateRunning {
			t.Fatalf("terminal committed before cleanup: state=%q err=%v", run.State(), err)
		}
	}
	fixture.signalExit()
	if _, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("exit-terminal", "exit-terminal"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if run.State() != domain.RunStateAwaitingVerification {
		t.Fatalf("state=%q", run.State())
	}
	if fixture.supervisor.finalizeCalls != 1 {
		t.Fatalf("finalize=%d", fixture.supervisor.finalizeCalls)
	}
}

func TestStartReplayReturnsOriginalResultAfterTerminalAdvance(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	request := app.StartAttemptRequest{CommandMeta: meta("stable-start", "stable-start"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh}
	started, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.exit(t, started, "advance-exit", execution.TerminalResult{Kind: execution.TerminalRevisionRequired})
	replayed, err := fixture.service.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Run.Version() != started.Run.Version() || replayed.Run.State() != domain.RunStateRunning || replayed.Attempt.State() != domain.AttemptStateRunning || replayed.Session == nil || replayed.Session.ID != started.Session.ID {
		t.Fatalf("replay=%#v original=%#v", replayed, started)
	}
}

func TestHandleExitFinalizeFailureRetriesWithoutTerminalCAS(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady}
	fixture.supervisor.finalizeFailures = 1
	fixture.signalExit()
	request := app.ExitRequest{CommandMeta: meta("retry-finalize", "retry-finalize"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()}
	_, err := fixture.service.HandleExit(context.Background(), request)
	if err == nil {
		t.Fatal("expected finalize failure")
	}
	run, readErr := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if readErr != nil || run.State() != domain.RunStateRunning {
		t.Fatalf("terminal committed despite cleanup failure: state=%q err=%v", run.State(), readErr)
	}
	second, err := fixture.service.HandleExit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.State() != domain.RunStateAwaitingVerification || second.Replayed || fixture.supervisor.finalizeCalls != 2 {
		t.Fatalf("second=%#v finalize=%d", second, fixture.supervisor.finalizeCalls)
	}
}

func TestCommandAuthorizationAndServerBindingPrecedeMutation(t *testing.T) {
	fixture := newFixture(t)
	denied := errors.New("denied")
	service := app.NewService(app.Dependencies{Store: fixture.db, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: rejectAuthorizer{err: denied}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	request := app.PrepareRunRequest{CommandMeta: meta("auth", "auth"), RunID: fixture.run.ID(), ExpectedVersion: 0}
	if _, err := service.PrepareRun(context.Background(), request); !errors.Is(err, denied) {
		t.Fatalf("err=%v", err)
	}
	run, _ := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if run.Version() != 0 {
		t.Fatalf("version=%d", run.Version())
	}
	if _, err := fixture.service.PrepareRun(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.ActorID = "other"
	if _, err := fixture.service.PrepareRun(context.Background(), request); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("server binding replayed cross actor: %v", err)
	}
}

func TestStartOutcomeCommitUnknownLeavesDurableLaunchIntentForRecovery(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	wrapped := &nthFailStore{Store: fixture.db, failAt: 3, err: storecontract.ErrCommitUnknown}
	service := app.NewService(app.Dependencies{Store: wrapped, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	_, err := service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("unknown-outcome", "unknown-outcome"), RunID: fixture.run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if !errors.Is(err, storecontract.ErrCommitUnknown) {
		t.Fatalf("err=%v", err)
	}
	attempt, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if session.State != "starting" || session.ProcessIdentity != "pid:1" {
		t.Fatalf("session=%#v", session)
	}
	if err := service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("state=%q", run.State())
	}
}

func TestStartupRecoveryKeepsLegacyStartingAttemptWithoutSessionRecoverable(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	now := prepared.Run.UpdatedAt().Add(time.Second)
	if err := fixture.db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(context.Background(), fixture.run.ID())
		if err != nil {
			return err
		}
		next, err := run.Transition(domain.CommandStartAttempt, now)
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(context.Background(), fixture.run.ID())
		if err != nil {
			return err
		}
		nextAttempt, err := attempt.Transition(domain.AttemptEventStartAttempt)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(context.Background(), prepared.Run.Version(), next); err != nil {
			return err
		}
		return tx.SaveAttemptCAS(context.Background(), attempt.State(), nextAttempt)
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	attempt, _ := fixture.db.Reader().GetCurrentAttempt(context.Background(), fixture.run.ID())
	if run.State() != domain.RunStateRecoveryRequired || attempt.State() != domain.AttemptStateStarting {
		t.Fatalf("run=%q attempt=%q", run.State(), attempt.State())
	}
}

func TestStartupRecoveryDoesNotTreatLegacyExternalReferenceAsLaunchToken(t *testing.T) {
	fixture := newFixture(t)
	prepared := fixture.prepare(t)
	now := prepared.Run.UpdatedAt().Add(time.Second)
	sessionID := domain.NewRuntimeSessionID()
	if err := fixture.db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(context.Background(), fixture.run.ID())
		if err != nil {
			return err
		}
		next, err := run.Transition(domain.CommandStartAttempt, now)
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(context.Background(), fixture.run.ID())
		if err != nil {
			return err
		}
		nextAttempt, err := attempt.Transition(domain.AttemptEventStartAttempt)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(context.Background(), prepared.Run.Version(), next); err != nil {
			return err
		}
		if err := tx.SaveAttemptCAS(context.Background(), attempt.State(), nextAttempt); err != nil {
			return err
		}
		return tx.InsertRuntimeSession(context.Background(), storecontract.RuntimeSession{ID: sessionID, AttemptID: attempt.ID(), AdapterID: "fake", RuntimeKind: "process", ExternalReference: "legacy-ref", LaunchToken: "", WorkingRoot: fixture.room.WorkspaceRoot(), SecurityFingerprint: sha256.Sum256([]byte("legacy")), State: "starting", CreatedAt: now, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.supervisor.reconcileCalls != 0 || fixture.supervisor.reconcileLaunchCalls != 0 {
		t.Fatalf("identity reconciles=%d launch reconciles=%d", fixture.supervisor.reconcileCalls, fixture.supervisor.reconcileLaunchCalls)
	}
	run, _ := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("state=%q", run.State())
	}
}

func TestHandleExitRejectsMalformedTerminalAndPersistsFailureReason(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminalErr = execution.ErrResultContractInvalid
	fixture.signalExit()
	result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("bad-terminal", "bad-terminal"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired || result.Attempt.State() != domain.AttemptStateFailed {
		t.Fatalf("result=%#v", result)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte("result_contract_invalid")) {
		t.Fatalf("event=%s", events[len(events)-1].NormalizedJSON())
	}
}

func TestHandleExitNonzeroExitIgnoresSuccessfulResult(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}, TerminalFiles: execution.TerminalFiles{ExitCode: 9}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "malicious success", Outputs: []execution.WorkspaceArtifact{{Locator: "bad.txt", Description: "ignore"}}}
	fixture.signalExit()
	result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("nonzero", "nonzero"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired || fixture.adapter.terminalDecodeCalls != 0 || len(result.Artifacts) != 0 {
		t.Fatalf("result=%#v calls=%d", result, fixture.adapter.terminalDecodeCalls)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), result.Attempt.ID())
	if err != nil || session.State != "stopped" || session.ProcessIdentity == "" || session.StartedAt.IsZero() || session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		t.Fatalf("process-death session=%#v err=%v", session, err)
	}
	retry, err := fixture.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{
		CommandMeta: meta("retry-process-death", "retry-process-death"), RunID: result.Run.ID(), ExpectedVersion: result.Run.Version(),
		Reason: "Agent exited nonzero and cleanup was proven.", Instructions: "Start a fresh governed Agent Attempt.",
	})
	if err != nil || retry.Run.State() != domain.RunStateReady || retry.Attempt.Sequence() != 2 {
		t.Fatalf("process-death retry=%#v err=%v", retry, err)
	}
}

func TestHandleExitProjectsTrustedTerminationCauseBeforeTerminalDecode(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  execution.TerminationCause
		reason string
	}{
		{name: "output limit", cause: execution.TerminationOutputLimit, reason: "runtime_output_limit_exceeded"},
		{name: "deadline", cause: execution.TerminationDeadlineExceeded, reason: "attempt_timeout"},
		{name: "nonzero", cause: execution.TerminationExitNonzero, reason: "runtime_exit_nonzero"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			started := fixture.started(t)
			fixture.supervisor.drainOutcome = execution.DrainOutcome{
				Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0},
				EOF:           map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true},
				TerminalFiles: execution.TerminalFiles{ExitCode: 0, TerminationCause: test.cause},
			}
			fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "must not decode"}
			fixture.signalExit()
			result, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{
				CommandMeta: meta("typed-"+test.name, "typed-"+test.name), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Attempt.State() != domain.AttemptStateFailed || fixture.adapter.terminalDecodeCalls != 0 {
				t.Fatalf("typed result=%#v decode calls=%d", result, fixture.adapter.terminalDecodeCalls)
			}
			events, err := fixture.db.Reader().ListRunEvents(context.Background(), result.Run.ID())
			if err != nil || !bytes.Contains(events[len(events)-1].NormalizedJSON(), []byte(test.reason)) {
				t.Fatalf("typed terminal event=%s err=%v", events[len(events)-1].NormalizedJSON(), err)
			}
		})
	}
}

func TestSubmitTerminalCannotBypassDrainProof(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	if _, err := fixture.service.SubmitTerminal(context.Background(), app.SubmitTerminalRequest{CommandMeta: meta("bypass", "bypass"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), Result: execution.TerminalResult{Kind: execution.TerminalReviewReady}}); err == nil {
		t.Fatal("terminal bypass accepted")
	}
}

func TestMalformedStopOutcomeEntersDurableRecovery(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.stopOutcome = execution.StopOutcome{Kind: "bogus"}
	result, err := fixture.service.RequestIntervention(context.Background(), app.StopRequest{CommandMeta: meta("bad-stop", "bad-stop"), RunID: fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("state=%q", result.Run.State())
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if session.State != "lost" || session.Diagnostic == "" {
		t.Fatalf("session=%#v", session)
	}
}

func TestCancelCleanupFailureEntersDurableRecovery(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	fixture.supervisor.finalizeFailures = 1

	result, err := fixture.service.RequestCancel(context.Background(), app.StopRequest{
		CommandMeta: meta("cancel-cleanup-failure", "cancel-cleanup-failure"),
		RunID:       fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("state=%q", result.Run.State())
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if session.State != "lost" || !session.FinalizedAt.IsZero() || !strings.Contains(session.Diagnostic, "runtime cleanup failed: finalize failed") {
		t.Fatalf("session=%#v", session)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type() != "run.recovery_required" {
		t.Fatalf("last event=%q", events[len(events)-1].Type())
	}
}

func TestCancelTerminalCommitFailureRecoversAfterRestart(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	wrapped := &nthFailStore{Store: fixture.db, failAt: 4, err: storecontract.ErrCommitUnknown}
	service := app.NewService(app.Dependencies{
		Store: wrapped, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter},
		Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{},
	})

	_, err := service.RequestCancel(context.Background(), app.StopRequest{
		CommandMeta: meta("cancel-terminal-commit-failure", "cancel-terminal-commit-failure"),
		RunID:       fixture.run.ID(), ExpectedVersion: started.Run.Version(), Reason: "cancel",
	})
	if !errors.Is(err, storecontract.ErrCommitUnknown) {
		t.Fatalf("err=%v", err)
	}
	stopping, err := fixture.db.Reader().GetRun(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	session, err := fixture.db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stopping.State() != domain.RunStateStopping || session.State != "stopped" || session.FinalizedAt.IsZero() {
		t.Fatalf("run=%q session=%#v", stopping.State(), session)
	}
	reconcileCalls := fixture.supervisor.reconcileCalls
	finalizeCalls := fixture.supervisor.finalizeCalls
	fixture.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "must not reconcile finalized cleanup"}
	restarted := app.NewService(app.Dependencies{
		Store: fixture.db, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter},
		Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{},
	})
	result, err := restarted.RequestCancel(context.Background(), app.StopRequest{
		CommandMeta: meta("resume-cancel-after-restart", "resume-cancel-after-restart"),
		RunID:       fixture.run.ID(), ExpectedVersion: stopping.Version(), Reason: "cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State() != domain.RunStateCancelled || fixture.supervisor.reconcileCalls != reconcileCalls || fixture.supervisor.finalizeCalls != finalizeCalls {
		t.Fatalf("result=%q reconcile=%d/%d finalize=%d/%d", result.Run.State(), fixture.supervisor.reconcileCalls, reconcileCalls, fixture.supervisor.finalizeCalls, finalizeCalls)
	}
	attempt, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), fixture.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State() != domain.AttemptStateCancelled {
		t.Fatalf("attempt=%q", attempt.State())
	}
}

func TestCreateRoomAndTaskAreTransactionalAndIdempotent(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(ctx, dbDir+"/create.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{&fakeAdapter{}}, Supervisor: &fakeSupervisor{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	roomRequest := app.CreateRoomRequest{CommandMeta: meta("create-room", "create-room"), Name: "room", Description: "desc", WorkspaceRoot: t.TempDir()}
	first, err := service.CreateRoom(ctx, roomRequest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.CreateRoom(ctx, roomRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Room.ID() != first.Room.ID() {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	content := domain.TechnicalPlanContent{TechnicalSteps: []string{"Implement the bounded change."}, Decisions: []string{"Use the existing application seam."}, Risks: []string{"A stale page must conflict."}, Unknowns: []string{"No unresolved implementation unknowns."}}
	taskRequest := app.CreateTaskRequest{CommandMeta: meta("create-task", "create-task"), RoomID: first.Room.ID(), Title: "task", Goal: "goal", Criteria: []domain.AcceptanceCriterion{criterion}, RevisionIDs: []domain.ContextRevisionID{first.InitialRevision.ID()}, PlanContent: content}
	task, err := service.CreateTask(ctx, taskRequest)
	if err != nil {
		t.Fatal(err)
	}
	taskReplay, err := service.CreateTask(ctx, taskRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !taskReplay.Replayed || taskReplay.Task.ID() != task.Task.ID() || task.Draft.EditVersion() != 1 || taskReplay.Draft.ID() != task.Draft.ID() || taskReplay.Draft.TaskID() != task.Task.ID() || task.Draft.SelectionDigest() != task.Selection.Digest() {
		t.Fatalf("task=%#v replay=%#v", task, taskReplay)
	}
	firstEditContent := domain.TechnicalPlanContent{TechnicalSteps: []string{"Implement the first exact edit."}, Decisions: content.Decisions, Risks: content.Risks, Unknowns: content.Unknowns}
	firstSaveRequest := app.SaveTechnicalPlanDraftRequest{CommandMeta: meta("save-plan-first", "save-plan-first"), DraftID: task.Draft.ID(), ExpectedEditVersion: task.Draft.EditVersion(), Content: firstEditContent}
	firstSave, err := service.SaveTechnicalPlanDraft(ctx, firstSaveRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondEditContent := domain.TechnicalPlanContent{TechnicalSteps: []string{"Implement the second exact edit."}, Decisions: content.Decisions, Risks: content.Risks, Unknowns: content.Unknowns}
	secondSave, err := service.SaveTechnicalPlanDraft(ctx, app.SaveTechnicalPlanDraftRequest{CommandMeta: meta("save-plan-second", "save-plan-second"), DraftID: task.Draft.ID(), ExpectedEditVersion: firstSave.Draft.EditVersion(), Content: secondEditContent})
	if err != nil {
		t.Fatal(err)
	}
	firstSaveReplay, err := service.SaveTechnicalPlanDraft(ctx, firstSaveRequest)
	if err != nil || !firstSaveReplay.Replayed || firstSaveReplay.Draft.EditVersion() != firstSave.Draft.EditVersion() || firstSaveReplay.Draft.Content().TechnicalSteps[0] != firstEditContent.TechnicalSteps[0] {
		t.Fatalf("first save=%#v replay after later edit=%#v err=%v", firstSave, firstSaveReplay, err)
	}
	task.Draft = secondSave.Draft
	submissionRequest := app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta("submit-plan", "submit-plan"), DraftID: task.Draft.ID(), ExpectedEditVersion: task.Draft.EditVersion()}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, submissionRequest)
	if err != nil {
		t.Fatal(err)
	}
	submittedReplay, err := service.SubmitTechnicalPlanDraft(ctx, submissionRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !submittedReplay.Replayed || submittedReplay.Revision.ID() != submitted.Revision.ID() || submitted.Revision.SelectionDigest() != task.Selection.Digest() {
		t.Fatalf("submitted=%#v replay=%#v", submitted, submittedReplay)
	}
	reviewRequest := app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("accept-plan", "accept-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Plan is bounded."}
	accepted, err := service.ReviewTechnicalPlanRevision(ctx, reviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	acceptedReplay, err := service.ReviewTechnicalPlanRevision(ctx, reviewRequest)
	if err != nil || !acceptedReplay.Replayed || accepted.Binding == nil || accepted.Charter == nil || accepted.Snapshot == nil || accepted.Charter.AdapterID() != "fake" || acceptedReplay.Review.ID() != accepted.Review.ID() {
		t.Fatalf("accepted=%#v replay=%#v err=%v", accepted, acceptedReplay, err)
	}
	createdRun, err := service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("create-run", "create-run"), TaskID: task.Task.ID(), RevisionID: submitted.Revision.ID()})
	if err != nil || createdRun.Binding.SnapshotID() != accepted.Binding.SnapshotID() || createdRun.Run.CharterID() != accepted.Charter.ID() {
		t.Fatalf("created Run=%#v err=%v", createdRun, err)
	}
	prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-bound-run", "prepare-bound-run"), RunID: createdRun.Run.ID(), ExpectedVersion: createdRun.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Snapshot.ID() != accepted.Binding.SnapshotID() || prepared.Snapshot.Digest() != accepted.Binding.SnapshotDigest() || prepared.Attempt.ContextSnapshotID() != accepted.Binding.SnapshotID() || prepared.Attempt.ContextDigest() != accepted.Binding.SnapshotDigest() {
		t.Fatalf("PrepareRun substituted accepted context: binding=%#v snapshot=%s/%x attempt=%s/%x", accepted.Binding, prepared.Snapshot.ID(), prepared.Snapshot.Digest(), prepared.Attempt.ContextSnapshotID(), prepared.Attempt.ContextDigest())
	}
}

func TestAcceptedRunProposesMultipleCandidatesConfirmAndDismissThroughApplicationInterface(t *testing.T) {
	t.Skip("deferred to G2-M5 human Accept and candidate promotion")
	fixture := newFixture(t)
	started := fixture.started(t)
	terminal := execution.TerminalResult{
		Kind: execution.TerminalReviewReady, Summary: "Two reviewed candidate facts.",
		Outputs:            []execution.WorkspaceArtifact{{Locator: "report.md", Description: "ordinary output"}},
		ArtifactCandidates: []execution.WorkspaceArtifact{{Locator: "one.md", Description: "first"}, {Locator: "two.md", Description: "second"}},
		Checks:             []execution.DeclaredCheck{{CriterionID: fixture.task.Criteria()[0].ID().String(), Status: execution.CheckPass, Evidence: "verified"}},
	}
	terminalResult := fixture.exit(t, started, "candidate-terminal", terminal)
	if len(terminalResult.Artifacts) != 3 {
		t.Fatalf("terminal artifacts = %v", terminalResult.Artifacts)
	}
	if _, err := fixture.service.Review(context.Background(), app.ReviewRequest{
		CommandMeta: meta("reject-output-candidate", "reject-output-candidate"), RunID: fixture.run.ID(), ExpectedVersion: terminalResult.Run.Version(), Kind: domain.ReviewDecisionAccept,
		CandidateProposals: []app.CandidateProposal{{ArtifactID: terminalResult.Artifacts[0], Title: "Output is not a candidate", Body: "Must fail closed."}},
	}); !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("ordinary output accepted as Candidate: %v", err)
	}
	reviewed, err := fixture.service.Review(context.Background(), app.ReviewRequest{
		CommandMeta: meta("accept-candidates", "accept-candidates"), RunID: fixture.run.ID(), ExpectedVersion: terminalResult.Run.Version(), Kind: domain.ReviewDecisionAccept,
		CandidateProposals: []app.CandidateProposal{
			{ArtifactID: terminalResult.Artifacts[1], Title: "First candidate", Body: "First candidate body."},
			{ArtifactID: terminalResult.Artifacts[2], Title: "Second candidate", Body: "Second candidate body."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Candidates) != 2 || reviewed.Candidates[0].State() != domain.CandidateStatePending || reviewed.Candidates[1].State() != domain.CandidateStatePending {
		t.Fatalf("review candidates = %#v", reviewed.Candidates)
	}
	editRequest := app.EditCandidateRequest{CommandMeta: meta("edit-first", "edit-first"), CandidateID: reviewed.Candidates[0].ID(), ExpectedVersion: reviewed.Candidates[0].Version(), Title: "Edited first candidate", Body: "Human-edited first candidate body."}
	edited, err := fixture.service.EditCandidate(context.Background(), editRequest)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := fixture.service.DecideCandidate(context.Background(), app.DecideCandidateRequest{
		CommandMeta: meta("confirm-first", "confirm-first"), CandidateID: edited.Candidate.ID(), ExpectedVersion: edited.Candidate.Version(), Kind: domain.CandidateDecisionConfirm, Note: "confirmed",
	})
	if err != nil {
		t.Fatal(err)
	}
	dismissed, err := fixture.service.DecideCandidate(context.Background(), app.DecideCandidateRequest{
		CommandMeta: meta("dismiss-second", "dismiss-second"), CandidateID: reviewed.Candidates[1].ID(), ExpectedVersion: reviewed.Candidates[1].Version(), Kind: domain.CandidateDecisionDismiss, Note: "not durable Room context",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Revision == nil || confirmed.Candidate.State() != domain.CandidateStateConfirmed || dismissed.Revision != nil || dismissed.Candidate.State() != domain.CandidateStateDismissed {
		t.Fatalf("confirmed=%#v dismissed=%#v", confirmed, dismissed)
	}
	if dismissed.Decision.ActorID() != "owner" || dismissed.Decision.SessionID() != "session" {
		t.Fatalf("dismiss audit identity=%q/%q", dismissed.Decision.ActorID(), dismissed.Decision.SessionID())
	}
	persistedDismissal, err := fixture.db.Reader().GetCandidateDecision(context.Background(), dismissed.Candidate.ID())
	if err != nil || persistedDismissal.ActorID() != "owner" || persistedDismissal.SessionID() != "session" {
		t.Fatalf("persisted dismiss audit identity=%q/%q err=%v", persistedDismissal.ActorID(), persistedDismissal.SessionID(), err)
	}
	replayedEdit, err := fixture.service.EditCandidate(context.Background(), editRequest)
	if err != nil || !replayedEdit.Replayed || replayedEdit.Candidate.State() != domain.CandidateStatePending || replayedEdit.Candidate.Version() != edited.Candidate.Version() || replayedEdit.Candidate.Title() != edited.Candidate.Title() {
		t.Fatalf("edit replay drifted: original=%#v replay=%#v err=%v", edited, replayedEdit, err)
	}
	revisions, err := fixture.db.Reader().ListRoomRevisions(context.Background(), fixture.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Provenance().CandidateID != confirmed.Candidate.ID().String() {
		t.Fatalf("dismiss created a revision or confirm provenance was lost: %#v", revisions)
	}
}

func TestExecutionDecisionGatePersistsAnswerWithoutBecomingResultReview(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	request := app.RequestExecutionDecisionRequest{
		CommandMeta: meta("gate-request", "gate-request"), RunID: started.Run.ID(), ExpectedVersion: started.Run.Version(),
		Question: "Continue with frozen evidence?", Context: "The Fake Agent needs one bounded execution choice.",
		Options:        []domain.DecisionGateOption{{ID: "continue", Label: "Continue", Impact: "Finish the structured result."}, {ID: "stop", Label: "Stop", Impact: "Return revision required."}},
		Recommendation: "Continue.", Impact: "The answer controls execution, not result acceptance.",
	}
	created, err := fixture.service.RequestExecutionDecision(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Gate.Status() != domain.DecisionGateOpen || created.Gate.AttemptID() != started.Attempt.ID() {
		t.Fatalf("created gate = %#v", created.Gate)
	}
	resolved, err := fixture.service.ResolveExecutionDecision(context.Background(), app.ResolveExecutionDecisionRequest{
		CommandMeta: meta("gate-resolve", "gate-resolve"), GateID: created.Gate.ID(), RunID: started.Run.ID(), ExpectedVersion: started.Run.Version(), OptionID: "continue", Note: "Use selected context only.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Gate.Status() != domain.DecisionGateResolved || resolved.Gate.SelectedOptionID() != "continue" || resolved.Gate.ActorID() != "owner" || resolved.Gate.SessionID() != "session" {
		t.Fatalf("resolved gate = %#v", resolved.Gate)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), started.Run.ID())
	if err != nil || run.State() != domain.RunStateRunning || run.Version() != started.Run.Version() {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	events, err := fixture.db.Reader().ListRunEvents(context.Background(), run.ID())
	if err != nil || len(events) < 2 || events[len(events)-2].Type() != "decision.requested" || events[len(events)-1].Type() != "decision.resolved" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if _, err := fixture.db.Reader().GetLatestReviewForRun(context.Background(), run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("execution decision created a result review: %v", err)
	}
}

func TestTerminalResultCannotBypassOpenExecutionDecisionGate(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	_, err := fixture.service.RequestExecutionDecision(context.Background(), app.RequestExecutionDecisionRequest{
		CommandMeta: meta("terminal-gate-request", "terminal-gate-request"), RunID: started.Run.ID(), ExpectedVersion: started.Run.Version(),
		Question: "Continue?", Context: "Execution must wait for the persisted answer.",
		Options:        []domain.DecisionGateOption{{ID: "continue", Label: "Continue", Impact: "Finish."}, {ID: "stop", Label: "Stop", Impact: "Do not finish."}},
		Recommendation: "Continue.", Impact: "This controls execution only.",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	fixture.adapter.terminal = execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "must not commit"}
	fixture.signalExit()
	_, err = fixture.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta("terminal-gate-exit", "terminal-gate-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err == nil || !errors.Is(err, app.ErrInvalidCommand) || !strings.Contains(err.Error(), "open execution Decision Gate") {
		t.Fatalf("HandleExit error = %v", err)
	}
	run, err := fixture.db.Reader().GetRun(context.Background(), started.Run.ID())
	if err != nil || run.State() != domain.RunStateRunning || run.Version() != started.Run.Version() {
		t.Fatalf("run mutated after blocked terminal: run=%#v err=%v", run, err)
	}
	attempt, err := fixture.db.Reader().GetCurrentAttempt(context.Background(), started.Run.ID())
	if err != nil || attempt.State() != domain.AttemptStateRunning {
		t.Fatalf("attempt mutated after blocked terminal: attempt=%#v err=%v", attempt, err)
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), attempt.ID())
	if err != nil || projection.Summary.Body != "" || len(projection.Artifacts) != 0 || len(projection.Checks) != 0 || len(projection.Unknowns) != 0 {
		t.Fatalf("terminal projection committed while Gate was open: projection=%#v err=%v", projection, err)
	}
}

func TestTerminalContextConsumptionFailsClosedWhenItDoesNotMatchFrozenSnapshot(t *testing.T) {
	fixture := newFixture(t)
	started := fixture.started(t)
	candidateID := domain.NewContextRevisionID().String()
	result := fixture.exit(t, started, "invalid-context-consumption", execution.TerminalResult{
		Kind: execution.TerminalReviewReady, Summary: "must fail closed",
		ContextConsumption: &execution.ContextConsumption{
			SnapshotID: started.Attempt.ContextSnapshotID().String(), SnapshotDigest: fmt.Sprintf("%x", started.Attempt.ContextDigest()),
			CandidateRevisionID: candidateID, IncludedRevisionIDs: []string{candidateID}, Derivation: "sha256:not-the-confirmed-content",
		},
	})
	if result.Run.State() != domain.RunStateRecoveryRequired {
		t.Fatalf("run state = %q, want recovery_required", result.Run.State())
	}
	projection, err := fixture.db.Reader().GetTerminalProjection(context.Background(), started.Attempt.ID())
	if err != nil || projection.ContextConsumption != nil {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
}

func TestTwoServiceCallersConcurrentConfirmCreateExactlyOneRevision(t *testing.T) {
	t.Skip("deferred to G2-M5 human Accept and candidate promotion")
	fixture := newFixture(t)
	started := fixture.started(t)
	terminal := execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "candidate", ArtifactCandidates: []execution.WorkspaceArtifact{{Locator: "candidate.md", Description: "candidate"}}, Checks: []execution.DeclaredCheck{{CriterionID: fixture.task.Criteria()[0].ID().String(), Status: execution.CheckPass, Evidence: "verified"}}}
	terminalResult := fixture.exit(t, started, "concurrent-candidate-terminal", terminal)
	reviewed, err := fixture.service.Review(context.Background(), app.ReviewRequest{CommandMeta: meta("accept-concurrent-candidate", "accept-concurrent-candidate"), RunID: fixture.run.ID(), ExpectedVersion: terminalResult.Run.Version(), Kind: domain.ReviewDecisionAccept, CandidateProposals: []app.CandidateProposal{{ArtifactID: terminalResult.Artifacts[0], Title: "Concurrent candidate", Body: "Only one confirmation may win."}}})
	if err != nil || len(reviewed.Candidates) != 1 {
		t.Fatalf("reviewed=%#v err=%v", reviewed, err)
	}
	secondService := app.NewService(app.Dependencies{Store: fixture.db, Context: app.ContextAssembler{}, Agents: fakeRegistry{fixture.adapter}, Supervisor: fixture.supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: fixture.charter.CreatedAt()}, IDs: app.RandomIDs{}})
	services := []*app.Service{fixture.service, secondService}
	start := make(chan struct{})
	results := make(chan error, len(services))
	var wait sync.WaitGroup
	for index, service := range services {
		wait.Add(1)
		go func(index int, service *app.Service) {
			defer wait.Done()
			<-start
			_, err := service.DecideCandidate(context.Background(), app.DecideCandidateRequest{CommandMeta: meta(fmt.Sprintf("concurrent-confirm-%d", index), fmt.Sprintf("concurrent-confirm-%d", index)), CandidateID: reviewed.Candidates[0].ID(), ExpectedVersion: reviewed.Candidates[0].Version(), Kind: domain.CandidateDecisionConfirm, Note: "confirmed"})
			results <- err
		}(index, service)
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, failures int
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	revisions, err := fixture.db.Reader().ListRoomRevisions(context.Background(), fixture.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := fixture.db.Reader().GetCandidateDecision(context.Background(), reviewed.Candidates[0].ID())
	if err != nil || successes != 1 || failures != 1 || len(revisions) != 1 || decision.Kind() != domain.CandidateDecisionConfirm {
		t.Fatalf("successes=%d failures=%d revisions=%d decision=%#v err=%v", successes, failures, len(revisions), decision, err)
	}
}

type fixture struct {
	t          *testing.T
	db         *sqlite.Store
	service    *app.Service
	adapter    *fakeAdapter
	supervisor *fakeSupervisor
	room       domain.Room
	task       domain.Task
	charter    domain.RunCharter
	run        domain.AgentRun
	dbPath     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := dbDir + "/chora.db"
	db, err := openLatestSQLiteTestStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	adapter := &fakeAdapter{}
	supervisor := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}}}
	service := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{adapter}, Supervisor: supervisor, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: now}, IDs: app.RandomIDs{}})
	createdRoom, err := service.CreateRoom(ctx, app.CreateRoomRequest{CommandMeta: meta("fixture-room", "fixture-room"), Name: "room", Description: "fixture", WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	createdTask, err := service.CreateTask(ctx, app.CreateTaskRequest{
		CommandMeta: meta("fixture-task", "fixture-task"), RoomID: createdRoom.Room.ID(), Title: "task", Goal: "goal",
		Criteria: []domain.AcceptanceCriterion{criterion}, RevisionIDs: []domain.ContextRevisionID{createdRoom.InitialRevision.ID()},
		PlanContent: domain.TechnicalPlanContent{TechnicalSteps: []string{"Implement the accepted plan."}, Decisions: []string{"Use the bound Fake adapter."}, Risks: []string{"A stale binding must fail closed."}, Unknowns: []string{"No unresolved fixture unknowns."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta("fixture-submit", "fixture-submit"), DraftID: createdTask.Draft.ID(), ExpectedEditVersion: createdTask.Draft.EditVersion()})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("fixture-accept", "fixture-accept"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Fixture plan accepted."})
	if err != nil || accepted.Charter == nil {
		t.Fatalf("accept fixture plan: result=%#v err=%v", accepted, err)
	}
	createdRun, err := service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("fixture-run", "fixture-run"), TaskID: createdTask.Task.ID(), RevisionID: submitted.Revision.ID()})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, db: db, service: service, adapter: adapter, supervisor: supervisor, room: createdRoom.Room, task: createdTask.Task, charter: *accepted.Charter, run: createdRun.Run, dbPath: dbPath}
}

func updateRuntimeBindingRaw(t *testing.T, path string, id domain.RuntimeSessionID, column string, value any) {
	t.Helper()
	allowed := map[string]bool{"external_reference": true, "working_root": true, "security_fingerprint": true}
	if !allowed[column] {
		t.Fatalf("unsupported column %s", column)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE runtime_sessions SET `+column+`=? WHERE id=?`, value, id.String()); err != nil {
		t.Fatal(err)
	}
}

func updateAttemptExternalRaw(t *testing.T, path, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE attempts SET external_session=? WHERE sequence=2`, value); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) prepare(t *testing.T) app.PrepareRunResult {
	r, err := f.service.PrepareRun(context.Background(), app.PrepareRunRequest{CommandMeta: meta("prepare", "prepare"), RunID: f.run.ID(), ExpectedVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) awaitingReview(t *testing.T) domain.AgentRun {
	s := f.started(t)
	o := f.exit(t, s, "terminal", execution.TerminalResult{Kind: execution.TerminalReviewReady})
	return o.Run
}

func (f *fixture) exit(t *testing.T, started app.StartAttemptResult, key string, terminal execution.TerminalResult) app.SubmitTerminalResult {
	f.supervisor.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
	f.adapter.terminal = terminal
	f.signalExit()
	result, err := f.service.HandleExit(context.Background(), app.ExitRequest{CommandMeta: meta(key, key), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *fixture) signalExit() {
	f.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: f.supervisor.invocation.LaunchToken()}
	f.supervisor.sink.Exited()
}

func (f *fixture) started(t *testing.T) app.StartAttemptResult {
	p := f.prepare(t)
	s, err := f.service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("start", "start"), RunID: f.run.ID(), ExpectedVersion: p.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type failingStore struct {
	app.Store
	err error
}

func (f *failingStore) WithinWriteTx(context.Context, func(storecontract.WriteTx) error) error {
	return f.err
}

type nthFailStore struct {
	app.Store
	mu            sync.Mutex
	calls, failAt int
	err           error
}

type toggleFailStore struct {
	app.Store
	mu   sync.Mutex
	fail bool
	err  error
}

func (store *toggleFailStore) setFail(fail bool) {
	store.mu.Lock()
	store.fail = fail
	store.mu.Unlock()
}

func (store *toggleFailStore) WithinWriteTx(ctx context.Context, fn func(storecontract.WriteTx) error) error {
	store.mu.Lock()
	fail, err := store.fail, store.err
	store.mu.Unlock()
	if fail {
		return err
	}
	return store.Store.WithinWriteTx(ctx, fn)
}

func (f *nthFailStore) WithinWriteTx(ctx context.Context, fn func(storecontract.WriteTx) error) error {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call == f.failAt {
		return f.err
	}
	return f.Store.WithinWriteTx(ctx, fn)
}

var errCloseTask = errors.New("close task failed")

type closeFailStore struct{ app.Store }

func (f *closeFailStore) WithinWriteTx(ctx context.Context, fn func(storecontract.WriteTx) error) error {
	return f.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return fn(closeFailTx{WriteTx: tx}) })
}

type closeFailTx struct{ storecontract.WriteTx }

func (closeFailTx) CloseTaskForAcceptedRun(context.Context, domain.TaskID, domain.RunID, uint64) error {
	return errCloseTask
}

func meta(key, digest string) app.CommandMeta {
	return app.CommandMeta{ActorID: "owner", SessionID: "session", IdempotencyKey: key, RequestDigest: sha256.Sum256([]byte(digest))}
}

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { c.now = c.now.Add(time.Second); return c.now }

type fakeRegistry struct{ adapter *fakeAdapter }

func (r fakeRegistry) Get(string) (execution.AgentAdapter, error) { return r.adapter, nil }

type staticRegistry struct{ adapter execution.AgentAdapter }

func (r staticRegistry) Get(string) (execution.AgentAdapter, error) { return r.adapter, nil }

type fakeAdapter struct {
	mu                  sync.Mutex
	id                  string
	prepareCalls        int
	startRequest        execution.StartRequest
	onPrepare           func()
	decode              []execution.NormalizedEvent
	decodeEvent         func(execution.EventChunk) (execution.DecodedChunk, error)
	consumed            int
	terminal            execution.TerminalResult
	terminalErr         error
	terminalDecodeCalls int
	resumeCalls         int
	resumeRequest       execution.ResumeRequest
	resumeMode          execution.ResumeMode
	fingerprint         execution.RuntimeFingerprint
}

type bindingFingerprintFakeAdapter struct {
	*fakeAdapter
	bindingFingerprint execution.RuntimeFingerprint
	bindingErr         error
	binding            domain.AgentExecutionProfileBinding
	bindingCalls       int
}

func (adapter *bindingFingerprintFakeAdapter) FingerprintForBinding(_ context.Context, binding domain.AgentExecutionProfileBinding) (execution.RuntimeFingerprint, error) {
	adapter.bindingCalls++
	adapter.binding = binding
	return adapter.bindingFingerprint, adapter.bindingErr
}

func (a *fakeAdapter) ID() string {
	if a.id != "" {
		return a.id
	}
	return "fake"
}
func (a *fakeAdapter) Capabilities() execution.AdapterCapabilities {
	mode := a.resumeMode
	if mode == "" {
		mode = execution.ResumeExplicitSession
	}
	return execution.AdapterCapabilities{ResumeMode: mode}
}
func (a *fakeAdapter) PrepareStart(_ context.Context, request execution.StartRequest) (execution.Invocation, error) {
	a.mu.Lock()
	a.prepareCalls++
	a.startRequest = request
	fn := a.onPrepare
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
	params := execution.InvocationParams{AdapterID: a.ID(), Executable: "/fake", WorkingRoot: request.WorkspaceRoot, LaunchToken: request.LaunchToken}
	if binding := request.Attempt.AgentExecutionProfileBinding(); binding.Bound() {
		target, err := execution.NewExecutionTarget(a.ID(), binding.ExecutionProvider())
		if err != nil {
			return execution.Invocation{}, err
		}
		params.Target = target
	}
	return execution.NewInvocation(params)
}
func (a *fakeAdapter) PrepareBoundStart(ctx context.Context, request execution.StartRequest) (execution.StartPreparation, error) {
	invocation, err := a.PrepareStart(ctx, request)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	fingerprint, err := a.Fingerprint(ctx)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	return execution.NewStartPreparation(invocation, fingerprint)
}
func (a *fakeAdapter) PrepareResume(_ context.Context, request execution.ResumeRequest) (execution.Invocation, error) {
	a.resumeCalls++
	a.resumeRequest = request
	return execution.NewInvocation(execution.InvocationParams{AdapterID: a.ID(), Executable: "/fake", WorkingRoot: request.Binding.WorkingRoot, LaunchToken: request.LaunchToken})
}
func (a *fakeAdapter) DecodeEvent(chunk execution.EventChunk) (execution.DecodedChunk, error) {
	if a.decodeEvent != nil {
		return a.decodeEvent(chunk)
	}
	consumed := a.consumed
	if consumed == 0 {
		consumed = len(chunk.Data)
	}
	return execution.DecodedChunk{Events: append([]execution.NormalizedEvent(nil), a.decode...), ConsumedBytes: consumed}, nil
}
func (a *fakeAdapter) DecodeTerminal(execution.TerminalFiles) (execution.TerminalResult, error) {
	a.terminalDecodeCalls++
	return a.terminal, a.terminalErr
}
func (a *fakeAdapter) Fingerprint(context.Context) (execution.RuntimeFingerprint, error) {
	if a.fingerprint.Valid() {
		return a.fingerprint, nil
	}
	return execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte("runtime")), Version: "test-v1"}, nil
}

type fakeSupervisor struct {
	mu                                    sync.Mutex
	startCalls, stopCalls, reconcileCalls int
	reconcileLaunchCalls                  int
	closeStdinCalls                       int
	closeStdinCheck                       func()
	finalizeCalls                         int
	outcome                               execution.StartOutcome
	stopOutcome                           execution.StopOutcome
	stopBlock                             chan struct{}
	reconcileCheck                        func()
	reconcileOutcome                      execution.ReconcileOutcome
	reconcileOutcomes                     []execution.ReconcileOutcome
	readChunks                            []execution.StreamChunk
	readOffsets                           []int64
	readLimits                            []int
	drainOutcome                          execution.DrainOutcome
	drainOutcomes                         []execution.DrainOutcome
	drainOffsets                          []execution.StreamOffsets
	drainLimits                           []int
	drainCalls                            int
	onDrain                               func(int)
	finalizeFailures                      int
	finalizeCheck                         func()
	sink                                  execution.RuntimeSink
	invocation                            execution.Invocation
}

func (s *fakeSupervisor) CloseStdin(context.Context, execution.RuntimeHandle) error {
	s.mu.Lock()
	s.closeStdinCalls++
	check := s.closeStdinCheck
	s.mu.Unlock()
	if check != nil {
		check()
	}
	return nil
}

func (s *fakeSupervisor) Start(_ context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	s.invocation = invocation
	s.sink = sink
	outcome := s.outcome
	if !outcome.LaunchToken.Valid() {
		outcome.LaunchToken = invocation.LaunchToken()
	}
	return outcome
}
func (s *fakeSupervisor) Stop(context.Context, execution.RuntimeHandle, execution.StopIntent) (execution.StopOutcome, error) {
	s.mu.Lock()
	s.stopCalls++
	block := s.stopBlock
	outcome := s.stopOutcome
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if outcome.Kind == "" {
		outcome.Kind = execution.StopConfirmed
	}
	return outcome, nil
}
func (s *fakeSupervisor) Reconcile(context.Context, execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	s.mu.Lock()
	s.reconcileCalls++
	check := s.reconcileCheck
	var scripted execution.ReconcileOutcome
	if len(s.reconcileOutcomes) > 0 {
		scripted = s.reconcileOutcomes[0]
		s.reconcileOutcomes = s.reconcileOutcomes[1:]
	}
	s.mu.Unlock()
	if check != nil {
		check()
	}
	if scripted.Kind != "" {
		if !scripted.LaunchToken.Valid() {
			scripted.LaunchToken = s.invocation.LaunchToken()
		}
		return scripted, nil
	}
	if s.reconcileOutcome.Kind != "" {
		outcome := s.reconcileOutcome
		if !outcome.LaunchToken.Valid() {
			outcome.LaunchToken = s.invocation.LaunchToken()
		}
		return outcome, nil
	}
	return execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: s.invocation.LaunchToken()}, nil
}
func (s *fakeSupervisor) ReconcileLaunch(_ context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	s.mu.Lock()
	s.reconcileLaunchCalls++
	outcome := s.reconcileOutcome
	s.mu.Unlock()
	if outcome.Kind == "" {
		outcome = execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle"}}
	}
	if !outcome.LaunchToken.Valid() {
		outcome.LaunchToken = launch
	}
	return outcome, nil
}
func (s *fakeSupervisor) Read(_ context.Context, _ execution.RuntimeHandle, _ execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readOffsets = append(s.readOffsets, offset)
	s.readLimits = append(s.readLimits, limit)
	if len(s.readChunks) == 0 {
		return execution.StreamChunk{NextOffset: offset}, nil
	}
	chunk := s.readChunks[0]
	s.readChunks = s.readChunks[1:]
	return chunk, nil
}
func (s *fakeSupervisor) Drain(_ context.Context, _ execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	s.mu.Lock()
	copyOffsets := execution.StreamOffsets{}
	for kind, offset := range offsets {
		copyOffsets[kind] = offset
	}
	s.drainOffsets = append(s.drainOffsets, copyOffsets)
	s.drainLimits = append(s.drainLimits, limit)
	s.drainCalls++
	call := s.drainCalls
	onDrain := s.onDrain
	if len(s.drainOutcomes) > 0 {
		outcome := s.drainOutcomes[0]
		s.drainOutcomes = s.drainOutcomes[1:]
		s.mu.Unlock()
		if onDrain != nil {
			onDrain(call)
		}
		return outcome, nil
	}
	s.mu.Unlock()
	if onDrain != nil {
		onDrain(call)
	}
	if s.drainOutcome.EOF == nil {
		return execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}, nil
	}
	return s.drainOutcome, nil
}
func (s *fakeSupervisor) Finalize(context.Context, execution.RuntimeHandle, execution.RetentionPolicy) error {
	s.mu.Lock()
	s.finalizeCalls++
	if s.finalizeFailures > 0 {
		s.finalizeFailures--
		s.mu.Unlock()
		return errors.New("finalize failed")
	}
	check := s.finalizeCheck
	s.mu.Unlock()
	if check != nil {
		check()
	}
	return nil
}

type fakePresence struct{}

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(context.Context, app.CommandAuthorizationRequest) error { return nil }

type rejectAuthorizer struct{ err error }

func (r rejectAuthorizer) Authorize(context.Context, app.CommandAuthorizationRequest) error {
	return r.err
}

func (fakePresence) AuthorizeReview(_ context.Context, request app.PresenceRequest) (domain.HumanReviewAuthorization, error) {
	return app.MintHumanReviewAuthorization(app.TrustedPresenceProof{HumanIssued: true, DeviceOwnerPresent: true}, request)
}
