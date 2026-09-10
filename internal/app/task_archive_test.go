package app_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestArchiveAndRestoreTaskAreReversibleAndIdempotent(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(ctx, dbDir+"/archive.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{&fakeAdapter{}}, Supervisor: &fakeSupervisor{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})

	room, err := service.CreateRoom(ctx, app.CreateRoomRequest{CommandMeta: meta("archive-room", "archive-room"), Name: "room", Description: "desc", WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	content := domain.TechnicalPlanContent{TechnicalSteps: []string{"step"}, Decisions: []string{"decision"}, Risks: []string{"risk"}, Unknowns: []string{"none"}}
	task, err := service.CreateTask(ctx, app.CreateTaskRequest{CommandMeta: meta("archive-task-create", "archive-task-create"), RoomID: room.Room.ID(), Title: "task", Goal: "goal", Criteria: []domain.AcceptanceCriterion{criterion}, RevisionIDs: []domain.ContextRevisionID{room.InitialRevision.ID()}, PlanContent: content})
	if err != nil {
		t.Fatal(err)
	}

	archived, err := service.ArchiveTask(ctx, app.ArchiveTaskRequest{CommandMeta: meta("archive-task", "archive-task"), TaskID: task.Task.ID()})
	if err != nil || !archived.Task.Archived() || archived.Task.ArchivedAt().IsZero() {
		t.Fatalf("archived=%#v err=%v", archived, err)
	}
	replay, err := service.ArchiveTask(ctx, app.ArchiveTaskRequest{CommandMeta: meta("archive-task", "archive-task"), TaskID: task.Task.ID()})
	if err != nil || !replay.Replayed || !replay.Task.Archived() {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err := service.ArchiveTask(ctx, app.ArchiveTaskRequest{CommandMeta: meta("archive-task-again", "archive-task-again"), TaskID: task.Task.ID()}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("archive conflict err=%v", err)
	}

	restored, err := service.RestoreTask(ctx, app.ArchiveTaskRequest{CommandMeta: meta("restore-task", "restore-task"), TaskID: task.Task.ID()})
	if err != nil || restored.Task.Archived() || !restored.Task.ArchivedAt().IsZero() {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	if _, err := service.RestoreTask(ctx, app.ArchiveTaskRequest{CommandMeta: meta("restore-task-again", "restore-task-again"), TaskID: task.Task.ID()}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("restore conflict err=%v", err)
	}

	persisted, err := db.Reader().GetTask(ctx, task.Task.ID())
	if err != nil || persisted.Archived() || !persisted.ArchivedAt().IsZero() {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}
