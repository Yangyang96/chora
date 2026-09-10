package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestProjectRoomsShareOneResourceAndRenameIndependently(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "checkout")
	fixture.registerRepo(repo, false)
	opened, err := fixture.svc.OpenProject(ctx, app.OpenProjectRequest{CommandMeta: meta("project-room-open", "open"), Locator: repo, Name: "Payments"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.svc.CreateProjectRoom(ctx, app.CreateProjectRoomRequest{CommandMeta: meta("project-room-create", "create"), ProjectID: opened.Project.Project.ID(), Name: "Refunds", Description: "Refund implementation"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Room.ProjectID() != opened.Project.Project.ID() || created.Room.ID() == opened.Project.Room.ID() {
		t.Fatalf("invalid Room ownership: %+v", created.Room)
	}
	firstBinding, err := fixture.db.Reader().GetRepositoryBinding(ctx, opened.Project.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	secondBinding, err := fixture.db.Reader().GetRepositoryBinding(ctx, created.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if firstBinding.LocalLocator() != secondBinding.LocalLocator() || firstBinding.Name() != secondBinding.Name() {
		t.Fatalf("Rooms do not project one resource: %#v %#v", firstBinding, secondBinding)
	}
	renamed, err := fixture.svc.RenameProject(ctx, app.RenameProjectRequest{CommandMeta: meta("project-rename", "rename"), ProjectID: opened.Project.Project.ID(), Name: "Commerce", ExpectedVersion: opened.Project.Project.Version()})
	if err != nil {
		t.Fatal(err)
	}
	room, err := fixture.svc.RenameRoom(ctx, app.RenameRoomRequest{CommandMeta: meta("room-rename", "rename"), RoomID: created.Room.ID(), Name: "Returns", Description: "Returns and refunds", ExpectedVersion: created.Room.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Project.Name() != "Commerce" || room.Name() != "Returns" || renamed.RepositoryBinding.Name() != "Payments" {
		t.Fatalf("rename crossed identities: project=%q room=%q resource=%q", renamed.Project.Name(), room.Name(), renamed.RepositoryBinding.Name())
	}
	revisions, err := fixture.db.Reader().ListRoomRevisions(ctx, created.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Revision().Body() == revisions[1].Revision().Body() {
		t.Fatalf("Room brief history=%#v", revisions)
	}
	view, err := fixture.svc.GetProjectByID(ctx, opened.Project.Project.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rooms) != 2 {
		t.Fatalf("rooms=%d", len(view.Rooms))
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	content := domain.TechnicalPlanContent{TechnicalSteps: []string{"Implement the Room-scoped task."}, Decisions: []string{"Keep Room ownership."}, Risks: []string{"Avoid cross-Room resume."}, Unknowns: []string{"None."}}
	task, err := fixture.svc.CreateTask(ctx, app.CreateTaskRequest{CommandMeta: meta("room-b-task", "task"), RoomID: created.Room.ID(), Title: "Refund task", Goal: "Implement refunds", Criteria: []domain.AcceptanceCriterion{criterion}, RevisionIDs: []domain.ContextRevisionID{created.InitialRevision.ID()}, PlanContent: content})
	if err != nil {
		t.Fatal(err)
	}
	view, err = fixture.svc.GetProjectByID(ctx, opened.Project.Project.ID())
	if err != nil {
		t.Fatal(err)
	}
	if view.Room.ID() != opened.Project.Room.ID() || view.CurrentAction == nil || view.CurrentAction.Target.RoomID != created.Room.ID() || view.CurrentAction.Target.TaskID != task.Task.ID() || view.TaskCounts.Total != 1 {
		t.Fatalf("Project aggregate lost Room B action: %#v", view)
	}
}

func TestArchivedProjectKeepsRoomStateAndRefusesNewRoom(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := t.TempDir()
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	archived, err := fixture.svc.ArchiveProject(ctx, app.ChangeProjectLifecycleRequest{CommandMeta: meta("project-archive", "archive"), ProjectID: opened.Project.Project.ID(), ExpectedVersion: opened.Project.Project.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Project.State() != domain.ProjectStateArchived || archived.Room.State() != domain.RoomStateActive {
		t.Fatalf("project=%q room=%q", archived.Project.State(), archived.Room.State())
	}
	_, err = fixture.svc.CreateProjectRoom(ctx, app.CreateProjectRoomRequest{CommandMeta: meta("archived-create", "create"), ProjectID: archived.Project.ID(), Name: "Blocked"})
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("create error=%v", err)
	}
	restored, err := fixture.svc.RestoreProject(ctx, app.ChangeProjectLifecycleRequest{CommandMeta: meta("project-restore", "restore"), ProjectID: archived.Project.ID(), ExpectedVersion: archived.Project.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Project.State() != domain.ProjectStateActive || restored.Room.State() != domain.RoomStateActive {
		t.Fatalf("restore changed Room lifecycle")
	}
}

func TestArchivedRoomCanBeRenamedWithoutRestoring(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := t.TempDir()
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	archived, err := fixture.svc.ArchiveRoom(ctx, app.ChangeRoomLifecycleRequest{CommandMeta: meta("room-archive-rename", "archive"), RoomID: opened.Project.Room.ID(), ExpectedVersion: opened.Project.Room.Version()})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := fixture.svc.RenameRoom(ctx, app.RenameRoomRequest{CommandMeta: meta("archived-room-rename", "rename"), RoomID: archived.Room.ID(), Name: "Archived General", Description: archived.Room.Description(), ExpectedVersion: archived.Room.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.State() != domain.RoomStateArchived || renamed.Name() != "Archived General" || renamed.ArchivedAt().IsZero() {
		t.Fatalf("renamed=%#v", renamed)
	}
}
