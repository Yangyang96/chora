package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
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
		root, err := os.MkdirTemp("", "chora-store-sqlite-template-")
		if err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		defer os.RemoveAll(root)
		if err := os.Chmod(root, 0o700); err != nil {
			latestSQLiteTestTemplate.err = err
			return
		}
		templatePath := filepath.Join(root, "chora.db")
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

func TestOpenMigratesToCurrentVersionAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "chora.db")
	db, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.SchemaVersion(context.Background()); err != nil || got != 42 {
		t.Fatalf("version=%d err=%v", got, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.SchemaVersion(context.Background()); err != nil || got != 42 {
		t.Fatalf("reopen version=%d err=%v", got, err)
	}
	if mode := os.FileMode(0o777) & fileMode(t, filepath.Dir(path)); mode != 0o700 {
		t.Fatalf("dir mode=%#o", mode)
	}
	if mode := os.FileMode(0o777) & fileMode(t, path); mode != 0o600 {
		t.Fatalf("db mode=%#o", mode)
	}
}

func TestCommitRunCommandIsAtomicAndIdempotent(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	next, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("command-1"))
	request := sha256.Sum256([]byte("request-1"))
	result, err := db.CommitRunCommand(context.Background(), storecontract.CommitRunCommandRequest{ExpectedVersion: 0, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "run.ready", Source: "test", OccurredAt: seeded.now.Add(time.Second), RecordedAt: seeded.now.Add(time.Second), NormalizedJSON: []byte(`{"state":"ready"}`)}, IdempotencyKeyHash: key, RequestDigest: request, Response: storecontract.Response{Status: 201, ContentType: "application/json", Body: []byte(`{"ok":true}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Event.Sequence() != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	replay, err := db.CommitRunCommand(context.Background(), storecontract.CommitRunCommandRequest{ExpectedVersion: 0, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "ignored", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: key, RequestDigest: request})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || string(replay.Response.Body) != `{"ok":true}` {
		t.Fatalf("bad replay: %#v", replay)
	}
	run, err := db.Reader().GetRun(context.Background(), seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Reader().ListRunEvents(context.Background(), seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if run.Version() != 1 || len(events) != 1 {
		t.Fatalf("partial/idempotency failure version=%d events=%d", run.Version(), len(events))
	}
	_, err = db.CommitRunCommand(context.Background(), storecontract.CommitRunCommandRequest{ExpectedVersion: 1, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "x", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: key, RequestDigest: sha256.Sum256([]byte("different"))})
	if !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestStaleRunVersionRollsBackEvent(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	next := seeded.run
	_, err := db.CommitRunCommand(context.Background(), storecontract.CommitRunCommandRequest{ExpectedVersion: 99, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "x", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: sha256.Sum256([]byte("k")), RequestDigest: sha256.Sum256([]byte("r"))})
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	events, err := db.Reader().ListRunEvents(context.Background(), seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("stale write persisted %d events", len(events))
	}
}

func TestOneActiveRunPerTask(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	run2, err := domain.NewAgentRun(domain.NewRunID(), seeded.run.TaskID(), seeded.run.CharterID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.InsertRun(context.Background(), run2) })
	if !errors.Is(err, storecontract.ErrActiveRunExists) {
		t.Fatalf("expected active run conflict, got %v", err)
	}
	accepted, err := seeded.run.Transition(domain.CommandCancelInactiveRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(context.Background(), 0, accepted) })
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.InsertRun(context.Background(), run2) })
	if err != nil {
		t.Fatalf("terminal run should release active slot: %v", err)
	}
}

func TestPromoteReviewedPersistsAndReplaysOriginalProvenance(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	accepted := seeded.run
	var event domain.RunEvent
	var review domain.ReviewDecision
	var err error
	artifact := storecontract.Artifact{ID: domain.NewArtifactID(), RunID: accepted.ID(), Kind: "patch", Locator: "file:///tmp/patch", CreatedAt: seeded.now.Add(time.Second)}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		accepted, review, err = advanceRunToAccepted(ctx, tx, seeded.run, seeded.now)
		if err != nil {
			return err
		}
		event, err = tx.AppendRunEvent(ctx, accepted.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "review.accepted", Source: "test", OccurredAt: seeded.now.Add(time.Second), RecordedAt: seeded.now.Add(time.Second), NormalizedJSON: []byte(`{}`)})
		if err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, artifact); err != nil {
			return err
		}
		return tx.InsertReview(ctx, review)
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.Reader().GetTask(ctx, accepted.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: task.RoomID(), Kind: domain.ContextKindDecision, RevisionNumber: 1, Title: "persist", Body: "body", CreatedAt: seeded.now.Add(2 * time.Second), UpdatedAt: seeded.now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	authorizationID, err := contextcore.ParseAuthorizationID("promotion_authorization_00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("promotion"))
	receipt, err := contextcore.RestoreAuthorizationReceipt(authorizationID, digest, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := contextcore.PromoteReviewedRequest{Revision: revision, ReviewDecisionID: review.ID(), RunID: accepted.ID(), ExpectedAcceptedRunVersion: accepted.Version(), ArtifactID: artifact.ID, EventID: event.ID(), CommandDigest: digest, Authorization: receipt}
	mismatch := request
	mismatch.ExpectedAcceptedRunVersion = accepted.Version() + 1
	rejected, err := db.PromoteReviewed(ctx, mismatch)
	if err != nil || rejected.Rejection != contextcore.PromotionRejectionVersionMismatch {
		t.Fatalf("version rejection=%#v err=%v", rejected, err)
	}
	if _, err := db.Reader().LookupRevisions(ctx, revision.RoomID(), []domain.ContextRevisionID{revision.RevisionID()}); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("rejected promotion left revision: %v", err)
	}
	result, err := db.PromoteReviewed(ctx, request)
	if err != nil || result.Outcome != contextcore.PromotionOutcomeApplied {
		t.Fatalf("apply result=%#v err=%v", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	replay, err := db.PromoteReviewed(ctx, request)
	if err != nil || replay.Outcome != contextcore.PromotionOutcomeAlreadyApplied {
		t.Fatalf("replay result=%#v err=%v", replay, err)
	}
	if replay.Record.AuthorizationID() != authorizationID || replay.Record.AuthorizationBinding() != digest {
		t.Fatal("replay lost original authorization provenance")
	}
	conflict := request
	conflict.CommandDigest = sha256.Sum256([]byte("different"))
	conflicted, err := db.PromoteReviewed(ctx, conflict)
	if err != nil || conflicted.Rejection != contextcore.PromotionRejectionIdempotencyConflict {
		t.Fatalf("conflict=%#v err=%v", conflicted, err)
	}
}

func TestContextRevisionsRemainAddressableAndSnapshotRenderRoundTrips(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	task, err := db.Reader().GetTask(ctx, seeded.run.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	entryID := domain.NewContextEntryID()
	rev1ID := domain.NewContextRevisionID()
	rev2ID := domain.NewContextRevisionID()
	rev1, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: rev1ID, RoomID: task.RoomID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "v1", Body: "one", CreatedAt: seeded.now, UpdatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: rev2ID, RoomID: task.RoomID(), Kind: domain.ContextKindBrief, RevisionNumber: 2, Supersedes: &rev1ID, Title: "v2", Body: "two", CreatedAt: seeded.now, UpdatedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRevision(ctx, rev1); err != nil {
			return err
		}
		return tx.InsertRevision(ctx, rev2)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Reader().LookupRevisions(ctx, task.RoomID(), []domain.ContextRevisionID{rev1ID, rev2ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body() != "one" || got[1].Body() != "two" {
		t.Fatalf("old revisions not addressable: %#v", got)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{rev1ID}, WorkspaceRoot: seeded.room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "snapshot", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"read": true}, Initiator: "test", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot contextcore.Snapshot
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		var err error
		snapshot, err = contextcore.NewAssembler(tx).Assemble(ctx, contextcore.AssembleRequest{SnapshotID: domain.NewContextSnapshotID(), Task: task, Charter: charter})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Reader().GetSnapshot(ctx, snapshot.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.CanonicalJSON(), snapshot.CanonicalJSON()) || !bytes.Equal(loaded.Markdown(), snapshot.Markdown()) {
		t.Fatal("snapshot render changed")
	}
}

func TestMigrationLedgerFailsClosedOnDriftGapAndNewerSchema(t *testing.T) {
	for _, tc := range []struct{ name, mutate string }{
		{"checksum drift", `UPDATE schema_migrations SET checksum=zeroblob(32) WHERE version=1`},
		{"gap", `DELETE FROM schema_migrations WHERE version=2`},
		{"newer", `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(43,'0043_future.sql',zeroblob(32),'2026-01-01T00:00:00Z')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data", "chora.db")
			db, err := openLatestSQLiteTestStore(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(tc.mutate); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(context.Background(), path)
			if err == nil {
				reopened.Close()
				t.Fatal("expected fail-closed migration error")
			}
		})
	}
}

func TestSQLiteSafetyPragmasIntegrityAndIndexShape(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	mode, err := db.JournalMode(ctx)
	if err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q err=%v", mode, err)
	}
	if err := db.CheckIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "orphan", "")
	orphan, err := domain.NewTask(domain.NewTaskID(), domain.NewRoomID(), "orphan", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTask(ctx, orphan) })
	if err == nil {
		t.Fatal("foreign key did not reject orphan task")
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var sqlText string
	if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='one_active_run_per_task'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if sqlText != "CREATE UNIQUE INDEX one_active_run_per_task ON runs(task_id) WHERE state NOT IN ('accepted', 'cancelled', 'completed')" {
		t.Fatalf("unexpected partial index: %s", sqlText)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(seeded.path + suffix); err == nil {
			if mode := fileMode(t, seeded.path+suffix).Perm(); mode&0o077 != 0 {
				t.Fatalf("%s mode=%#o", suffix, mode)
			}
		}
	}
}

func TestStatementFailureRollsBackRunEventAndIdempotency(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON run_events BEGIN SELECT RAISE(ABORT,'forced event failure'); END`); err != nil {
		t.Fatal(err)
	}
	next, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := storecontract.CommitRunCommandRequest{ExpectedVersion: 0, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "ready", Source: "test", OccurredAt: seeded.now.Add(time.Second), RecordedAt: seeded.now.Add(time.Second), NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: sha256.Sum256([]byte("trigger-key")), RequestDigest: sha256.Sum256([]byte("trigger-request")), Response: storecontract.Response{Status: 200, Body: []byte("ok")}}
	if _, err := db.CommitRunCommand(ctx, request); err == nil {
		t.Fatal("expected trigger failure")
	}
	run, err := db.Reader().GetRun(ctx, seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Reader().ListRunEvents(ctx, seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if run.Version() != 0 || len(events) != 0 {
		t.Fatalf("partial write version=%d events=%d", run.Version(), len(events))
	}
	if _, err := raw.Exec(`DROP TRIGGER fail_event`); err != nil {
		t.Fatal(err)
	}
	result, err := db.CommitRunCommand(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("failed transaction persisted idempotency response")
	}
}

func TestConcurrentSameExpectedVersionHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	next, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			<-start
			_, err := db.CommitRunCommand(ctx, storecontract.CommitRunCommandRequest{ExpectedVersion: 0, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "ready", Source: "test", OccurredAt: seeded.now.Add(time.Second), RecordedAt: seeded.now.Add(time.Second), NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: sha256.Sum256([]byte{byte(i + 1)}), RequestDigest: sha256.Sum256([]byte{byte(i + 10)}), Response: storecontract.Response{Status: 200}})
			results <- err
		}()
	}
	close(start)
	err1, err2 := <-results, <-results
	successes, conflicts := 0, 0
	for _, err := range []error{err1, err2} {
		if err == nil {
			successes++
		} else if errors.Is(err, storecontract.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
}

func TestStartupCandidatesOnlyIncludeLiveAttemptsOrRunningStoppingRuns(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	ready, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.RestoreAttempt(domain.AttemptRecord{ID: domain.NewAttemptID(), RunID: ready.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "codex", State: domain.AttemptStateStarting, CreatedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveRunCAS(ctx, 0, ready); err != nil {
			return err
		}
		return tx.InsertAttempt(ctx, attempt)
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := db.Reader().ListStartupCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Attempt == nil || candidates[0].Attempt.State() != domain.AttemptStateStarting {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestAppendRunEventOwnsContinuousPerRunSequence(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	var got []domain.RunEvent
	err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		for i := 0; i < 3; i++ {
			event, err := tx.AppendRunEvent(ctx, seeded.run.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "event", Source: "test", OccurredAt: seeded.now.Add(time.Duration(i) * time.Second), RecordedAt: seeded.now.Add(time.Duration(i) * time.Second), NormalizedJSON: []byte(`{}`)})
			if err != nil {
				return err
			}
			got = append(got, event)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range got {
		if event.Sequence() != int64(i+1) {
			t.Fatalf("event %d sequence=%d", i, event.Sequence())
		}
	}
	stored, err := db.Reader().ListRunEvents(ctx, seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 || stored[2].Sequence() != 3 {
		t.Fatalf("stored events=%#v", stored)
	}
}

func TestEventSequenceCannotBeMutatedThroughRunCASOrRawEventAPI(t *testing.T) {
	writeTxType := reflect.TypeOf((*storecontract.WriteTx)(nil)).Elem()
	if _, ok := writeTxType.MethodByName("InsertEvent"); ok {
		t.Fatal("WriteTx exposes raw InsertEvent sequence bypass")
	}
	method, ok := writeTxType.MethodByName("SaveRunCAS")
	if !ok {
		t.Fatal("SaveRunCAS missing")
	}
	if method.Type.NumIn() != 3 {
		t.Fatalf("SaveRunCAS accepts unexpected sequence argument: %s", method.Type)
	}
	eventDraftType := reflect.TypeOf(storecontract.EventDraft{})
	if _, ok := eventDraftType.FieldByName("Sequence"); ok {
		t.Fatal("EventDraft exposes caller-controlled sequence")
	}
}

func TestConcurrentAppendsHaveNoGaps(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	start := make(chan struct{})
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			<-start
			results <- db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				_, err := tx.AppendRunEvent(ctx, seeded.run.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "event", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)})
				return err
			})
		}()
	}
	close(start)
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.Reader().ListRunEvents(ctx, seeded.run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events=%d", len(events))
	}
	for i, event := range events {
		if event.Sequence() != int64(i+1) {
			t.Fatalf("gap at %d: %d", i, event.Sequence())
		}
	}
}

func TestDifferentRunsEachStartEventSequenceAtOne(t *testing.T) {
	fixture := newPromotionFixture(t)
	defer fixture.db.Close()
	mainEvents, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	otherEvents, err := fixture.db.Reader().ListRunEvents(context.Background(), fixture.otherRun.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(mainEvents) != 1 || mainEvents[0].Sequence() != 1 || len(otherEvents) != 1 || otherEvents[0].Sequence() != 1 {
		t.Fatalf("main=%#v other=%#v", mainEvents, otherEvents)
	}
}

func TestInsertCharterFailsClosedForMissingFrozenReferences(t *testing.T) {
	for _, tc := range []string{"selection", "confirmation", "exclusion"} {
		t.Run(tc, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "data", "chora.db")
			db, err := openLatestSQLiteTestStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
			task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "task", "goal", []domain.AcceptanceCriterion{criterion})
			if err != nil {
				t.Fatal(err)
			}
			entryID := domain.NewContextEntryID()
			revisionID := domain.NewContextRevisionID()
			revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: revisionID, RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "context", CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				if err := tx.InsertRoom(ctx, room); err != nil {
					return err
				}
				if err := tx.InsertTask(ctx, task); err != nil {
					return err
				}
				return tx.InsertRevision(ctx, revision)
			}); err != nil {
				t.Fatal(err)
			}
			selected := []domain.ContextRevisionID{revisionID}
			var confirmed []domain.ContextRevisionID
			var excluded []domain.ContextEntryID
			switch tc {
			case "selection":
				selected = []domain.ContextRevisionID{domain.NewContextRevisionID()}
			case "confirmation":
				confirmed = []domain.ContextRevisionID{domain.NewContextRevisionID()}
			case "exclusion":
				excluded = []domain.ContextEntryID{domain.NewContextEntryID()}
			}
			charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: selected, ConfirmedSensitiveRevisionIDs: confirmed, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", SensitiveExclusions: excluded, ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertCharter(ctx, charter) })
			if err == nil {
				t.Fatalf("missing %s was silently accepted", tc)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			for _, table := range []string{"run_charters", "charter_criteria", "charter_capabilities", "charter_context_selections", "charter_sensitive_confirmations", "charter_sensitive_exclusions"} {
				var count int
				query := fmt.Sprintf("SELECT count(*) FROM %s WHERE charter_id=?", table)
				if table == "run_charters" {
					query = "SELECT count(*) FROM run_charters WHERE id=?"
				}
				if err := raw.QueryRow(query, charter.ID().String()).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("%s left %d partial rows", table, count)
				}
			}
		})
	}
}

func TestGetCharterRestoresFrozenCriteriaAndSensitiveSelections(t *testing.T) {
	ctx := context.Background()
	db, err := openLatestSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "data", "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterionID := domain.NewCriterionID()
	taskCriterion, _ := domain.NewAcceptanceCriterion(criterionID, "task title", "")
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "task", "goal", []domain.AcceptanceCriterion{taskCriterion})
	if err != nil {
		t.Fatal(err)
	}
	confirmedRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "confirmed", Sensitive: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	excludedRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindConstraint, RevisionNumber: 1, Title: "excluded", Sensitive: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	frozenCriterion, _ := domain.NewAcceptanceCriterion(criterionID, "frozen title", "frozen description")
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: []domain.AcceptanceCriterion{frozenCriterion}, ContextRevisionIDs: []domain.ContextRevisionID{confirmedRevision.RevisionID(), excludedRevision.RevisionID()}, ConfirmedSensitiveRevisionIDs: []domain.ContextRevisionID{confirmedRevision.RevisionID()}, SensitiveExclusions: []domain.ContextEntryID{excludedRevision.EntryID()}, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"write": true}, Initiator: "test", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, confirmedRevision); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, excludedRevision); err != nil {
			return err
		}
		return tx.InsertCharter(ctx, charter)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Reader().GetCharter(ctx, charter.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Criteria(); len(got) != 1 || got[0].Title() != "frozen title" || got[0].Description() != "frozen description" {
		t.Fatalf("criteria=%#v", got)
	}
	if got := loaded.ConfirmedSensitiveRevisionIDs(); len(got) != 1 || got[0] != confirmedRevision.RevisionID() {
		t.Fatalf("confirmed=%v", got)
	}
	if got := loaded.SensitiveExclusions(); len(got) != 1 || got[0] != excludedRevision.EntryID() {
		t.Fatalf("excluded=%v", got)
	}
}

func TestPromoteReviewedRejectsInvalidPersistedProvenanceWithoutPartialRows(t *testing.T) {
	for _, tc := range []string{"review reject", "run nonaccepted", "artifact mismatch", "event mismatch", "room mismatch"} {
		t.Run(tc, func(t *testing.T) {
			fixture := newPromotionFixture(t)
			defer fixture.db.Close()
			request := fixture.request
			switch tc {
			case "review reject":
				request.ReviewDecisionID = fixture.rejectedReview.ID()
			case "run nonaccepted":
				request.RunID = fixture.otherRun.ID()
				request.ReviewDecisionID = fixture.otherReview.ID()
				request.ExpectedAcceptedRunVersion = 2
				request.ArtifactID = fixture.otherArtifact.ID
				request.EventID = fixture.otherEvent.ID()
			case "artifact mismatch":
				request.ArtifactID = fixture.otherArtifact.ID
			case "event mismatch":
				request.EventID = fixture.otherEvent.ID()
			case "room mismatch":
				request.Revision = fixture.otherRoomRevision
			}
			result, err := fixture.db.PromoteReviewed(context.Background(), request)
			if err != nil {
				t.Fatalf("unexpected adapter error: %v", err)
			}
			want := contextcore.PromotionRejectionProvenanceMismatch
			switch tc {
			case "review reject":
				want = contextcore.PromotionRejectionReviewNotAccepted
			case "run nonaccepted":
				want = contextcore.PromotionRejectionRunNotAccepted
			case "room mismatch":
				want = contextcore.PromotionRejectionRoomMismatch
			}
			if result.Outcome != contextcore.PromotionOutcomeRejected || result.Rejection != want {
				t.Fatalf("result=%#v want=%s", result, want)
			}
			assertNoPromotionRows(t, fixture.path, request.Revision)
		})
	}
}

func TestPromoteReviewedDatabaseLockIsKnownRollback(t *testing.T) {
	fixture := newPromotionFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	fixture.db, err = sqlite.OpenWithOptions(context.Background(), fixture.path, sqlite.OpenOptions{
		BusyTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.db.Close()
	raw, err := sql.Open("sqlite", fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := fixture.db.PromoteReviewed(context.Background(), fixture.request)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected lock error")
	}
	if elapsed >= time.Second {
		t.Fatalf("lock detection took %s", elapsed)
	}
	if result.Outcome != contextcore.PromotionOutcomeRetryableNotApplied {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, rollbackErr := raw.Exec(`ROLLBACK`); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	assertNoPromotionRows(t, fixture.path, fixture.request.Revision)
}

func TestOperationalRecordsRoundTripAndRejectOrphan(t *testing.T) {
	fixture := newPromotionFixture(t)
	defer fixture.db.Close()
	ctx := context.Background()
	runID := fixture.request.RunID
	eventID := fixture.request.EventID
	now := time.Now().UTC().Truncate(time.Microsecond)
	observation := storecontract.Observation{ID: domain.NewObservationID(), RunID: runID, Kind: "note", Body: "observed", SourceEventID: &eventID, CreatedAt: now}
	check := storecontract.Check{ID: domain.NewCheckID(), RunID: runID, Name: "tests", Status: "passed", Evidence: "ok", CreatedAt: now}
	intervention := storecontract.Intervention{ID: domain.NewInterventionID(), RunID: runID, Kind: "human", Reason: "inspect", RequestedBy: "owner", CreatedAt: now}
	handoff := storecontract.Handoff{ID: domain.NewHandoffID(), RunID: runID, FromActor: "agent", ToActor: "owner", Reason: "review", Status: "requested", CreatedAt: now}
	err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertObservation(ctx, observation); err != nil {
			return err
		}
		if err := tx.InsertCheck(ctx, check); err != nil {
			return err
		}
		if err := tx.InsertIntervention(ctx, intervention); err != nil {
			return err
		}
		return tx.InsertHandoff(ctx, handoff)
	})
	if err != nil {
		t.Fatal(err)
	}
	gotObservation, err := fixture.db.Reader().GetObservation(ctx, observation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotObservation.Body != observation.Body || gotObservation.SourceEventID == nil || *gotObservation.SourceEventID != eventID {
		t.Fatalf("observation=%#v", gotObservation)
	}
	gotCheck, err := fixture.db.Reader().GetCheck(ctx, check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotCheck.Status != "passed" {
		t.Fatalf("check=%#v", gotCheck)
	}
	gotIntervention, err := fixture.db.Reader().GetIntervention(ctx, intervention.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotIntervention.Reason != "inspect" {
		t.Fatalf("intervention=%#v", gotIntervention)
	}
	gotHandoff, err := fixture.db.Reader().GetHandoff(ctx, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotHandoff.ToActor != "owner" {
		t.Fatalf("handoff=%#v", gotHandoff)
	}
	orphan := observation
	orphan.ID = domain.NewObservationID()
	missingAttempt := domain.NewAttemptID()
	orphan.AttemptID = &missingAttempt
	if err := fixture.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertObservation(ctx, orphan) }); err == nil {
		t.Fatal("orphan attempt foreign key was accepted")
	}
}

func TestRuntimeStreamOffsetAdvancesMonotonically(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "codex", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	session := storecontract.RuntimeSession{ID: domain.NewRuntimeSessionID(), AttemptID: attempt.ID(), AdapterID: "codex", RuntimeKind: "process", Version: 0, WorkingRoot: seeded.room.WorkspaceRoot(), SecurityFingerprint: sha256.Sum256([]byte("security")), State: "running", CreatedAt: seeded.now, UpdatedAt: seeded.now}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		if err := tx.InsertRuntimeSession(ctx, session); err != nil {
			return err
		}
		if err := tx.AdvanceRuntimeStreamOffset(ctx, session.ID, storecontract.RuntimeStreamStdout, 0, 12, false); err != nil {
			return err
		}
		return tx.AdvanceRuntimeStreamOffset(ctx, session.ID, storecontract.RuntimeStreamStdout, 12, 20, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	offset, err := db.Reader().GetRuntimeStreamOffset(ctx, session.ID, storecontract.RuntimeStreamStdout)
	if err != nil {
		t.Fatal(err)
	}
	if offset.Offset != 20 || !offset.EOF {
		t.Fatalf("offset=%#v", offset)
	}
	for _, tc := range []struct {
		name           string
		expected, next int64
	}{{"backward", 20, 19}, {"stale", 12, 21}} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				return tx.AdvanceRuntimeStreamOffset(ctx, session.ID, storecontract.RuntimeStreamStdout, tc.expected, tc.next, true)
			})
			if !errors.Is(err, storecontract.ErrVersionConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRuntimeSessionCASRejectsLaunchTokenMutation(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "codex", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	session := storecontract.RuntimeSession{ID: domain.NewRuntimeSessionID(), AttemptID: attempt.ID(), AdapterID: "codex", RuntimeKind: "process", LaunchToken: "launch-1", WorkingRoot: seeded.room.WorkspaceRoot(), SecurityFingerprint: sha256.Sum256([]byte("security")), State: "starting", CreatedAt: seeded.now, UpdatedAt: seeded.now}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		return tx.InsertRuntimeSession(ctx, session)
	}); err != nil {
		t.Fatal(err)
	}
	next := session
	next.Version = 1
	next.LaunchToken = "launch-2"
	next.UpdatedAt = seeded.now.Add(time.Second)
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveRuntimeSessionCAS(ctx, 0, next) })
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("launch token mutation err=%v", err)
	}
}

func TestRuntimeSessionCASRejectsResumeBindingMutation(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "codex", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	runtimeFP := sha256.Sum256([]byte("runtime"))
	security := sha256.Sum256([]byte("security"))
	session := storecontract.RuntimeSession{ID: domain.NewRuntimeSessionID(), AttemptID: attempt.ID(), AdapterID: "codex", RuntimeKind: "process", ExternalReference: "session-1", LaunchToken: "launch", WorkingRoot: seeded.room.WorkspaceRoot(), SecurityFingerprint: security, RuntimeFingerprint: runtimeFP, RuntimeVersion: "1", State: "running", CreatedAt: seeded.now, UpdatedAt: seeded.now}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		return tx.InsertRuntimeSession(ctx, session)
	}); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*storecontract.RuntimeSession){func(s *storecontract.RuntimeSession) { s.ExternalReference = "session-2" }, func(s *storecontract.RuntimeSession) { s.WorkingRoot = "/tmp/other" }, func(s *storecontract.RuntimeSession) { s.SecurityFingerprint = sha256.Sum256([]byte("other")) }, func(s *storecontract.RuntimeSession) { s.RuntimeFingerprint = sha256.Sum256([]byte("other")) }, func(s *storecontract.RuntimeSession) { s.RuntimeVersion = "2" }}
	for index, mutate := range mutations {
		next := session
		next.Version = 1
		next.UpdatedAt = seeded.now.Add(time.Second)
		mutate(&next)
		err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveRuntimeSessionCAS(ctx, 0, next) })
		if !errors.Is(err, storecontract.ErrVersionConflict) {
			t.Fatalf("mutation %d err=%v", index, err)
		}
	}
}

type promotionFixture struct {
	db                          *sqlite.Store
	path                        string
	request                     contextcore.PromoteReviewedRequest
	rejectedReview, otherReview domain.ReviewDecision
	otherRun                    domain.AgentRun
	otherArtifact               storecontract.Artifact
	otherEvent                  domain.RunEvent
	otherRoomRevision           domain.RoomContextRevision
}

func newPromotionFixture(t *testing.T) promotionFixture {
	t.Helper()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	accepted := seeded.run
	mainArtifact := storecontract.Artifact{ID: domain.NewArtifactID(), RunID: accepted.ID(), Kind: "patch", Locator: "file:///main", CreatedAt: seeded.now}
	var acceptedReview domain.ReviewDecision
	var rejectedReview domain.ReviewDecision
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "other", "")
	otherTask, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "other", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	otherCharter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: otherTask.ID(), TaskGoal: otherTask.Goal(), Criteria: otherTask.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{seeded.revision.RevisionID()}, WorkspaceRoot: seeded.room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := domain.NewAgentRun(domain.NewRunID(), otherTask.ID(), otherCharter.ID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	otherReview, err := domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: domain.NewReviewDecisionID(), RunID: otherRun.ID(), Kind: domain.ReviewDecisionAccept, ExpectedRunVersion: 1, DecidedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	otherArtifact := storecontract.Artifact{ID: domain.NewArtifactID(), RunID: otherRun.ID(), Kind: "patch", Locator: "file:///other", CreatedAt: seeded.now}
	var mainEvent, otherEvent domain.RunEvent
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		accepted, acceptedReview, err = advanceRunToAccepted(ctx, tx, seeded.run, seeded.now)
		if err != nil {
			return err
		}
		rejectedReview, err = domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: domain.NewReviewDecisionID(), RunID: accepted.ID(), Kind: domain.ReviewDecisionReject, ExpectedRunVersion: acceptedReview.ExpectedRunVersion(), Comment: "reject", DecidedAt: acceptedReview.DecidedAt()})
		if err != nil {
			return err
		}
		mainEvent, err = tx.AppendRunEvent(ctx, accepted.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "accepted", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)})
		if err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, mainArtifact); err != nil {
			return err
		}
		if err := tx.InsertReview(ctx, acceptedReview); err != nil {
			return err
		}
		if err := tx.InsertReview(ctx, rejectedReview); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, otherTask); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, otherCharter); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, otherRun); err != nil {
			return err
		}
		otherEvent, err = tx.AppendRunEvent(ctx, otherRun.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "other", Source: "test", OccurredAt: seeded.now, RecordedAt: seeded.now, NormalizedJSON: []byte(`{}`)})
		if err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, otherArtifact); err != nil {
			return err
		}
		return tx.InsertReview(ctx, otherReview)
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	otherRoom, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "other room", WorkspaceRoot: filepath.Join(t.TempDir(), "other"), CreatedAt: seeded.now, UpdatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRoom(ctx, otherRoom) }); err != nil {
		t.Fatal(err)
	}
	otherRoomRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: otherRoom.ID(), Kind: domain.ContextKindDecision, RevisionNumber: 1, Title: "wrong room", CreatedAt: seeded.now, UpdatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("promotion direct"))
	authorizationID, err := contextcore.ParseAuthorizationID("promotion_authorization_11223344556677889900aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := contextcore.RestoreAuthorizationReceipt(authorizationID, digest, seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: seeded.room.ID(), Kind: domain.ContextKindDecision, RevisionNumber: 1, Title: "promoted", CreatedAt: seeded.now, UpdatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	return promotionFixture{db: db, path: seeded.path, request: contextcore.PromoteReviewedRequest{Revision: revision, ReviewDecisionID: acceptedReview.ID(), RunID: accepted.ID(), ExpectedAcceptedRunVersion: accepted.Version(), ArtifactID: mainArtifact.ID, EventID: mainEvent.ID(), CommandDigest: digest, Authorization: receipt}, rejectedReview: rejectedReview, otherReview: otherReview, otherRun: otherRun, otherArtifact: otherArtifact, otherEvent: otherEvent, otherRoomRevision: otherRoomRevision}
}

func advanceRunToAccepted(ctx context.Context, tx storecontract.WriteTx, run domain.AgentRun, at time.Time) (domain.AgentRun, domain.ReviewDecision, error) {
	commands := []domain.CommandKind{domain.CommandPrepareRun, domain.CommandStartAttempt, domain.CommandSubmitAgentReport, domain.CommandStartVerification, domain.CommandVerificationPassed}
	for i, command := range commands {
		next, err := run.Transition(command, at.Add(time.Duration(i+1)*time.Second))
		if err != nil {
			return domain.AgentRun{}, domain.ReviewDecision{}, err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
			return domain.AgentRun{}, domain.ReviewDecision{}, err
		}
		run = next
	}
	review, err := domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: domain.NewReviewDecisionID(), RunID: run.ID(), Kind: domain.ReviewDecisionAccept, ExpectedRunVersion: run.Version(), DecidedAt: at.Add(6 * time.Second)})
	if err != nil {
		return domain.AgentRun{}, domain.ReviewDecision{}, err
	}
	accepted, err := run.ApplyReview(review)
	if err != nil {
		return domain.AgentRun{}, domain.ReviewDecision{}, err
	}
	if err := tx.SaveRunCAS(ctx, run.Version(), accepted); err != nil {
		return domain.AgentRun{}, domain.ReviewDecision{}, err
	}
	return accepted, review, nil
}

func assertNoPromotionRows(t *testing.T, path string, revision domain.RoomContextRevision) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, query := range []string{`SELECT count(*) FROM context_entries WHERE id=?`, `SELECT count(*) FROM context_revisions WHERE id=?`, `SELECT count(*) FROM context_promotions WHERE revision_id=?`} {
		id := revision.RevisionID().String()
		if strings.Contains(query, "context_entries") {
			id = revision.EntryID().String()
		}
		var count int
		if err := raw.QueryRow(query, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("partial row for %s", id)
		}
	}
}

type seed struct {
	now      time.Time
	run      domain.AgentRun
	room     domain.Room
	task     domain.Task
	charter  domain.RunCharter
	revision domain.RoomContextRevision
	path     string
}

func openSeeded(t *testing.T) (*sqlite.Store, seed) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	path := filepath.Join(t.TempDir(), "data", "chora.db")
	db, err := openLatestSQLiteTestStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "task", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "seed context", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{contextRevision.RevisionID()}, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, contextRevision); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		return tx.InsertRun(ctx, run)
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	room, err = db.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, seed{now: now, run: run, room: room, task: task, charter: charter, revision: contextRevision, path: path}
}

func assembleSeedSnapshot(t *testing.T, db *sqlite.Store, seeded seed) contextcore.Snapshot {
	t.Helper()
	var snapshot contextcore.Snapshot
	err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		var err error
		snapshot, err = contextcore.NewAssembler(tx).Assemble(context.Background(), contextcore.AssembleRequest{SnapshotID: domain.NewContextSnapshotID(), Task: seeded.task, Charter: seeded.charter})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
