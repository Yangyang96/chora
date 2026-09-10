package sqlite_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestRoomWorkspaceProjectsCurrentPlanningAndLatestRunBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)

	draft, err := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{
		ID: domain.NewTechnicalPlanDraftID(), TaskID: seeded.task.ID(), SelectionDigest: sha256.Sum256([]byte("selection")),
		Content: domain.TechnicalPlanContent{
			TechnicalSteps: []string{"implement"}, Decisions: []string{"set queries"},
			Risks: []string{"drift"}, Unknowns: []string{"none"},
		},
		CreatedAt: seeded.now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	closed, revision, err := draft.Submit(draft.EditVersion(), domain.NewTechnicalPlanRevisionID(), false, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	review, err := domain.NewTechnicalPlanReview(domain.NewTechnicalPlanReviewParams{
		ID: domain.NewTechnicalPlanReviewID(), RevisionID: revision.ID(), TaskID: seeded.task.ID(),
		Kind: domain.TechnicalPlanReviewAccept, Reviewer: "owner", Note: "accepted", DecidedAt: seeded.now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := domain.NewTechnicalPlanAcceptanceBinding(domain.TechnicalPlanAcceptanceBindingRecord{
		TaskID: seeded.task.ID(), RevisionID: revision.ID(), ReviewID: review.ID(), SnapshotID: snapshot.ID(),
		SnapshotDigest: snapshot.Digest(), BoundAt: seeded.now.Add(4 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	runBinding, err := domain.NewTechnicalPlanRunBinding(domain.TechnicalPlanRunBindingRecord{
		RunID: seeded.run.ID(), TaskID: seeded.task.ID(), RevisionID: revision.ID(), CharterID: seeded.charter.ID(),
		SnapshotID: snapshot.ID(), SnapshotDigest: snapshot.Digest(), BoundAt: seeded.now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		if err := tx.SubmitTechnicalPlanDraftCAS(ctx, draft.EditVersion(), closed, revision); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanReviewCAS(ctx, review); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanAcceptance(ctx, acceptance); err != nil {
			return err
		}
		return tx.InsertTechnicalPlanRunBinding(ctx, runBinding)
	}); err != nil {
		t.Fatal(err)
	}

	workspace, err := db.Reader().GetRoomWorkspace(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Tasks) != 1 {
		t.Fatalf("Tasks=%d want=1", len(workspace.Tasks))
	}
	item := workspace.Tasks[0]
	if item.OpenDraftID.Valid() || item.LatestRevisionID != revision.ID() || item.LatestRevisionNumber != revision.RevisionNumber() ||
		!item.LatestRevisionSubmittedAt.Equal(revision.SubmittedAt()) || item.LatestPlanReviewID != review.ID() ||
		item.LatestPlanReviewRevisionID != revision.ID() || item.LatestPlanReviewKind != review.Kind() ||
		!item.LatestPlanReviewDecidedAt.Equal(review.DecidedAt()) || item.AcceptedRevisionID != revision.ID() ||
		item.LatestRun == nil || item.LatestRun.ID() != seeded.run.ID() || item.LatestRunBindingRevisionID != revision.ID() {
		t.Fatalf("planning projection=%#v", item)
	}
}

func TestRoomWorkspaceDoesNotAttachAnOlderReviewToANewerUnreviewedRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	content := domain.TechnicalPlanContent{TechnicalSteps: []string{"one"}, Decisions: []string{"exact"}, Risks: []string{"drift"}, Unknowns: []string{"none"}}
	draft, err := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{ID: domain.NewTechnicalPlanDraftID(), TaskID: seeded.task.ID(), SelectionDigest: sha256.Sum256([]byte("selection-review-lineage")), Content: content, CreatedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	closed, firstRevision, err := draft.Submit(draft.EditVersion(), domain.NewTechnicalPlanRevisionID(), false, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstReview, err := domain.NewTechnicalPlanReview(domain.NewTechnicalPlanReviewParams{ID: domain.NewTechnicalPlanReviewID(), RevisionID: firstRevision.ID(), TaskID: seeded.task.ID(), Kind: domain.TechnicalPlanReviewRequestRevision, Reviewer: "owner", Note: "revise", DecidedAt: seeded.now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	successor, err := domain.NewTechnicalPlanSuccessorDraft(domain.NewTechnicalPlanDraftID(), firstRevision, seeded.now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	editedSuccessor, err := successor.Edit(successor.EditVersion(), domain.TechnicalPlanContent{TechnicalSteps: []string{"two"}, Decisions: []string{"exact"}, Risks: []string{"bounded"}, Unknowns: []string{"none"}}, seeded.now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	closedSuccessor, secondRevision, err := editedSuccessor.Submit(editedSuccessor.EditVersion(), domain.NewTechnicalPlanRevisionID(), false, seeded.now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		if err := tx.SubmitTechnicalPlanDraftCAS(ctx, draft.EditVersion(), closed, firstRevision); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanReviewCAS(ctx, firstReview); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanDraft(ctx, successor); err != nil {
			return err
		}
		if err := tx.SaveTechnicalPlanDraftCAS(ctx, successor.EditVersion(), editedSuccessor); err != nil {
			return err
		}
		return tx.SubmitTechnicalPlanDraftCAS(ctx, editedSuccessor.EditVersion(), closedSuccessor, secondRevision)
	}); err != nil {
		t.Fatal(err)
	}
	workspace, err := db.Reader().GetRoomWorkspace(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Tasks) != 1 {
		t.Fatalf("Tasks=%d", len(workspace.Tasks))
	}
	item := workspace.Tasks[0]
	if item.LatestRevisionID != secondRevision.ID() || item.LatestPlanReviewID.Valid() || item.LatestPlanReviewRevisionID.Valid() || item.LatestPlanReviewKind != "" {
		t.Fatalf("latest unreviewed Revision inherited older Review: %#v", item)
	}
}

func TestRoomWorkspaceAndRunHistoryAreCompleteAndStablyOrdered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()

	criteria := []domain.AcceptanceCriterion{
		mustCriterion(t, "first"),
		mustCriterion(t, "second"),
	}
	otherTask, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "other task", "show every Task", criteria)
	if err != nil {
		t.Fatal(err)
	}
	foreignRoom, err := domain.NewRoom(domain.RoomParams{
		ID: domain.NewRoomID(), Name: "foreign room", WorkspaceRoot: filepath.Join(t.TempDir(), "foreign"),
		CreatedAt: seeded.now, UpdatedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, otherTask); err != nil {
			return err
		}
		return tx.InsertRoom(ctx, foreignRoom)
	}); err != nil {
		t.Fatal(err)
	}

	cancelledSeed, err := seeded.run.Transition(domain.CommandCancelInactiveRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRunCAS(ctx, seeded.run.Version(), cancelledSeed)
	}); err != nil {
		t.Fatal(err)
	}

	const extraRuns = 24
	runs := make([]domain.AgentRun, 0, extraRuns+1)
	runs = append(runs, cancelledSeed)
	eventCounts := map[domain.RunID]int{cancelledSeed.ID(): 0}
	latestActivity := seeded.now
	for index := 0; index < extraRuns; index++ {
		updatedAt := seeded.now.Add(time.Duration(index+2) * time.Second)
		run, err := domain.RestoreAgentRun(domain.AgentRunRecord{
			ID: domain.NewRunID(), TaskID: seeded.task.ID(), CharterID: seeded.charter.ID(),
			State: domain.RunStateCancelled, Version: 1, CreatedAt: seeded.now, UpdatedAt: updatedAt, TerminalAt: updatedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
		count := index % 4
		eventCounts[run.ID()] = count
		if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			if err := tx.InsertRun(ctx, run); err != nil {
				return err
			}
			for eventIndex := 0; eventIndex < count; eventIndex++ {
				at := seeded.now.Add(time.Duration(100+index*4+eventIndex) * time.Second)
				latestActivity = at
				if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{
					ID: domain.NewEventID(), Type: "test.event", Source: "test",
					OccurredAt: at, RecordedAt: at, NormalizedJSON: []byte(`{"ok":true}`),
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	newestCreatedAt := seeded.now.Add(time.Hour)
	newest, err := domain.RestoreAgentRun(domain.AgentRunRecord{
		ID: domain.NewRunID(), TaskID: seeded.task.ID(), CharterID: seeded.charter.ID(),
		State: domain.RunStateCancelled, Version: 1, CreatedAt: newestCreatedAt, UpdatedAt: newestCreatedAt, TerminalAt: newestCreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRun(ctx, newest) }); err != nil {
		t.Fatal(err)
	}
	runs = append(runs, newest)
	eventCounts[newest.ID()] = 0
	latestActivity = newestCreatedAt

	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt().Equal(runs[j].CreatedAt()) {
			return runs[i].ID().String() < runs[j].ID().String()
		}
		return runs[i].CreatedAt().After(runs[j].CreatedAt())
	})
	history, err := db.Reader().ListTaskRunHistory(ctx, seeded.room.ID(), seeded.task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != len(runs) {
		t.Fatalf("history length=%d want=%d", len(history), len(runs))
	}
	for index, summary := range history {
		if summary.Run.ID() != runs[index].ID() {
			t.Fatalf("history[%d]=%s want=%s", index, summary.Run.ID(), runs[index].ID())
		}
		if summary.EventCount != eventCounts[summary.Run.ID()] {
			t.Fatalf("history[%d] event count=%d want=%d", index, summary.EventCount, eventCounts[summary.Run.ID()])
		}
	}

	emptyHistory, err := db.Reader().ListTaskRunHistory(ctx, seeded.room.ID(), otherTask.ID())
	if err != nil || len(emptyHistory) != 0 {
		t.Fatalf("empty history=%#v err=%v", emptyHistory, err)
	}

	workspace, err := db.Reader().GetRoomWorkspace(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Room.Room.ID() != seeded.room.ID() || workspace.Room.TaskCounts.Total != 2 || workspace.Room.TaskCounts.Open != 2 || workspace.Room.TaskCounts.Terminal != 0 {
		t.Fatalf("Room summary=%#v", workspace.Room)
	}
	if workspace.Room.LastActivityAt.Before(latestActivity) {
		t.Fatalf("Room last activity=%s want at least %s", workspace.Room.LastActivityAt, latestActivity)
	}
	if len(workspace.Tasks) != 2 {
		t.Fatalf("Task count=%d want=2", len(workspace.Tasks))
	}
	items := make(map[domain.TaskID]storecontract.TaskWorkspaceItem, len(workspace.Tasks))
	for _, item := range workspace.Tasks {
		items[item.Task.ID()] = item
	}
	seedItem, ok := items[seeded.task.ID()]
	if !ok || seedItem.LatestRun == nil {
		t.Fatalf("seed Task projection=%#v", seedItem)
	}
	if seedItem.RunCount != len(runs) || seedItem.ActiveRunCount != 0 || seedItem.LatestRun.ID() != runs[0].ID() {
		t.Fatalf("seed Run summary count=%d active=%d latest=%v want latest=%s", seedItem.RunCount, seedItem.ActiveRunCount, seedItem.LatestRun, runs[0].ID())
	}
	otherItem, ok := items[otherTask.ID()]
	if !ok || otherItem.RunCount != 0 || otherItem.ActiveRunCount != 0 || otherItem.LatestRun != nil || len(otherItem.Task.Criteria()) != len(criteria) {
		t.Fatalf("other Task projection=%#v", otherItem)
	}
}

func TestWorkspaceReadsRejectRoomTaskRunOwnershipMismatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()

	otherRoom, err := domain.NewRoom(domain.RoomParams{
		ID: domain.NewRoomID(), Name: "other room", WorkspaceRoot: filepath.Join(t.TempDir(), "other"),
		CreatedAt: seeded.now, UpdatedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "other Task", "separate owner", []domain.AcceptanceCriterion{mustCriterion(t, "done")})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, otherRoom); err != nil {
			return err
		}
		return tx.InsertTask(ctx, otherTask)
	}); err != nil {
		t.Fatal(err)
	}
	workspace, err := db.Reader().GetRoomWorkspace(ctx, seeded.room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Tasks) != 2 || workspace.Tasks[0].RunCount+workspace.Tasks[1].RunCount != 1 {
		t.Fatalf("initial workspace=%#v", workspace.Tasks)
	}
	var seededItem storecontract.TaskWorkspaceItem
	for _, item := range workspace.Tasks {
		if item.Task.ID() == seeded.task.ID() {
			seededItem = item
		}
	}
	if seededItem.LatestRun == nil || seededItem.LatestRun.ID() != seeded.run.ID() || seededItem.ActiveRunCount != 1 {
		t.Fatalf("active Run projection=%#v", seededItem)
	}

	if _, err := db.Reader().GetRoomWorkspace(ctx, domain.NewRoomID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("missing Room workspace err=%v", err)
	}
	for _, ids := range []struct {
		room domain.RoomID
		task domain.TaskID
	}{
		{otherRoom.ID(), seeded.task.ID()},
		{seeded.room.ID(), domain.NewTaskID()},
	} {
		if _, err := db.Reader().ListTaskRunHistory(ctx, ids.room, ids.task); !errors.Is(err, storecontract.ErrNotFound) {
			t.Fatalf("history room=%s task=%s err=%v", ids.room, ids.task, err)
		}
	}
	for _, ids := range []struct {
		room domain.RoomID
		task domain.TaskID
		run  domain.RunID
	}{
		{otherRoom.ID(), seeded.task.ID(), seeded.run.ID()},
		{seeded.room.ID(), otherTask.ID(), seeded.run.ID()},
		{seeded.room.ID(), seeded.task.ID(), domain.NewRunID()},
	} {
		if _, err := db.Reader().GetTaskRun(ctx, ids.room, ids.task, ids.run); !errors.Is(err, storecontract.ErrNotFound) {
			t.Fatalf("Run room=%s task=%s run=%s err=%v", ids.room, ids.task, ids.run, err)
		}
	}
	loaded, err := db.Reader().GetTaskRun(ctx, seeded.room.ID(), seeded.task.ID(), seeded.run.ID())
	if err != nil || loaded.ID() != seeded.run.ID() || loaded.TaskID() != seeded.task.ID() {
		t.Fatalf("exact Run=%#v err=%v", loaded, err)
	}
}

func mustCriterion(t *testing.T, title string) domain.AcceptanceCriterion {
	t.Helper()
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), title, "")
	if err != nil {
		t.Fatal(err)
	}
	return criterion
}
