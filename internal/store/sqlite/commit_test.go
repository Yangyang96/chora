package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var latestInternalSQLiteTestTemplate struct {
	sync.Once
	data []byte
	err  error
}

func openLatestInternalSQLiteTestStore(ctx context.Context, path string, hook func() error) (*Store, error) {
	latestInternalSQLiteTestTemplate.Do(func() {
		root, err := os.MkdirTemp("", "chora-internal-sqlite-template-")
		if err != nil {
			latestInternalSQLiteTestTemplate.err = err
			return
		}
		defer os.RemoveAll(root)
		if err := os.Chmod(root, 0o700); err != nil {
			latestInternalSQLiteTestTemplate.err = err
			return
		}
		templatePath := filepath.Join(root, "chora.db")
		store, err := Open(context.Background(), templatePath)
		if err != nil {
			latestInternalSQLiteTestTemplate.err = err
			return
		}
		if err := store.Close(); err != nil {
			latestInternalSQLiteTestTemplate.err = err
			return
		}
		latestInternalSQLiteTestTemplate.data, latestInternalSQLiteTestTemplate.err = os.ReadFile(templatePath)
	})
	if latestInternalSQLiteTestTemplate.err != nil {
		return nil, fmt.Errorf("prepare latest internal SQLite test template: %w", latestInternalSQLiteTestTemplate.err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create internal SQLite test directory: %w", err)
	}
	if err := os.WriteFile(path, latestInternalSQLiteTestTemplate.data, 0o600); err != nil {
		return nil, fmt.Errorf("copy latest internal SQLite test template: %w", err)
	}
	if hook != nil {
		return openWithCommitHook(ctx, path, hook)
	}
	return Open(ctx, path)
}

func TestPrecommitPermissionFailureRollsBackWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "chora.db")
	permissionErr := errors.New("permission verification failed")
	store, err := openLatestInternalSQLiteTestStore(ctx, path, func() error { return permissionErr })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRoom(ctx, room) })
	if !errors.Is(err, permissionErr) {
		t.Fatalf("err=%v", err)
	}
	if _, err := store.Reader().GetRoom(ctx, room.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("precommit failure persisted room: %v", err)
	}
}

func TestCommitRunCommandPrecommitFailureRollsBackAggregateEventAndResponse(t *testing.T) {
	ctx := context.Background()
	store, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "data", "chora.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "context", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{revision.RevisionID()}, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		return tx.InsertRun(ctx, run)
	}); err != nil {
		t.Fatal(err)
	}
	next, err := run.Transition(domain.CommandPrepareRun, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := storecontract.CommitRunCommandRequest{ExpectedVersion: 0, NextRun: next, Event: storecontract.EventDraft{ID: domain.NewEventID(), Type: "ready", Source: "test", OccurredAt: now.Add(time.Second), RecordedAt: now.Add(time.Second), NormalizedJSON: []byte(`{}`)}, IdempotencyKeyHash: sha256.Sum256([]byte("key")), RequestDigest: sha256.Sum256([]byte("request")), Response: storecontract.Response{Status: 200, Body: []byte("ok")}}
	permissionErr := errors.New("permission verification failed")
	beforeCommit := store.beforeCommit
	store.beforeCommit = func() error { return permissionErr }
	_, err = store.CommitRunCommand(ctx, request)
	if !errors.Is(err, permissionErr) {
		t.Fatalf("err=%v", err)
	}
	stored, err := store.Reader().GetRun(ctx, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Reader().ListRunEvents(ctx, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version() != 0 || len(events) != 0 {
		t.Fatalf("partial version=%d events=%d", stored.Version(), len(events))
	}
	store.beforeCommit = beforeCommit
	result, err := store.CommitRunCommand(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("precommit failure persisted idempotency response")
	}
}

func TestPromoteReviewedPrecommitFailureRollsBackRevisionAndPromotion(t *testing.T) {
	ctx := context.Background()
	permissionErr := errors.New("permission verification failed")
	failCommit := false
	store, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "data", "chora.db"), func() error {
		if failCommit {
			return permissionErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", WorkspaceRoot: filepath.Join(t.TempDir(), "workspace"), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "task", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	baseRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindBrief, RevisionNumber: 1, Title: "context", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{baseRevision.RevisionID()}, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), now)
	if err != nil {
		t.Fatal(err)
	}
	artifact := storecontract.Artifact{ID: domain.NewArtifactID(), RunID: run.ID(), Kind: "patch", Locator: "file:///patch", CreatedAt: now}
	var review domain.ReviewDecision
	var event domain.RunEvent
	if err := store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, baseRevision); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		for index, command := range []domain.CommandKind{domain.CommandPrepareRun, domain.CommandStartAttempt, domain.CommandSubmitAgentReport, domain.CommandStartVerification, domain.CommandVerificationPassed} {
			next, err := run.Transition(command, now.Add(time.Duration(index+1)*time.Second))
			if err != nil {
				return err
			}
			if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
				return err
			}
			run = next
		}
		review, err = domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: domain.NewReviewDecisionID(), RunID: run.ID(), Kind: domain.ReviewDecisionAccept, ExpectedRunVersion: run.Version(), DecidedAt: now.Add(6 * time.Second)})
		if err != nil {
			return err
		}
		accepted, err := run.ApplyReview(review)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), accepted); err != nil {
			return err
		}
		run = accepted
		event, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "accepted", Source: "test", OccurredAt: now, RecordedAt: now, NormalizedJSON: []byte(`{}`)})
		if err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, artifact); err != nil {
			return err
		}
		return tx.InsertReview(ctx, review)
	}); err != nil {
		t.Fatal(err)
	}
	promotedRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: room.ID(), Kind: domain.ContextKindDecision, RevisionNumber: 1, Title: "promoted", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	commandDigest := sha256.Sum256([]byte("promotion"))
	authorizationID, err := contextcore.ParseAuthorizationID("promotion_authorization_11223344556677889900aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := contextcore.RestoreAuthorizationReceipt(authorizationID, commandDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	failCommit = true
	result, err := store.PromoteReviewed(ctx, contextcore.PromoteReviewedRequest{Revision: promotedRevision, ReviewDecisionID: review.ID(), RunID: run.ID(), ExpectedAcceptedRunVersion: run.Version(), ArtifactID: artifact.ID, EventID: event.ID(), CommandDigest: commandDigest, Authorization: authorization})
	if !errors.Is(err, permissionErr) {
		t.Fatalf("err=%v", err)
	}
	if result.Outcome != contextcore.PromotionOutcomeRetryableNotApplied {
		t.Fatalf("result=%#v", result)
	}
	for _, query := range []string{`SELECT count(*) FROM context_revisions WHERE id=?`, `SELECT count(*) FROM context_promotions WHERE revision_id=?`} {
		var count int
		if err := store.db.QueryRowContext(ctx, query, promotedRevision.RevisionID().String()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("precommit failure left %d rows for %q", count, query)
		}
	}
}
