package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

func TestExecutionSettingsPersistCASAndRemainProjectScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "execution-settings.db")
	db, err := openLatestSQLiteTestStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	firstProject, _, firstTask := insertExecutionSettingsProject(t, ctx, db, "First", now)
	secondProject, _, _ := insertExecutionSettingsProject(t, ctx, db, "Second", now.Add(time.Second))
	initial := domain.ProjectExecutionSettings{
		ProjectID: firstProject.ID(), Version: 1, AgentExecutionProfile: domain.AgentExecutionProfileTrustedLocal,
		UpdatedAt: now.Add(2 * time.Second),
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectExecutionSettings(ctx, 0, initial)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Reader().GetProjectExecutionSettings(ctx, secondProject.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("second Project read error=%v", err)
	}
	next := initial
	next.Version = 2
	next.AgentExecutionProfile = domain.AgentExecutionProfileIsolatedLocal
	next.UpdatedAt = now.Add(3 * time.Second)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectExecutionSettings(ctx, 1, next)
	}); err != nil {
		t.Fatal(err)
	}
	conflicting := next
	conflicting.Version = 2
	conflicting.UpdatedAt = now.Add(4 * time.Second)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectExecutionSettings(ctx, 1, conflicting)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("CAS error=%v", err)
	}
	taskSettings := domain.TaskExecutionSettings{
		TaskID: firstTask.ID(), ProjectID: firstProject.ID(), ProjectVersion: next.Version,
		AgentExecutionProfile: next.AgentExecutionProfile, EnvironmentSource: domain.ExecutionSettingsSourceProject,
		ModelSource: domain.ExecutionSettingsSourceDefault, NativeCapabilitiesJSON: `{"skills":["review"],"mcp":[]}`,
		CreatedAt: now.Add(5 * time.Second),
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTaskExecutionSettings(ctx, taskSettings)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	gotProject, err := reopened.Reader().GetProjectExecutionSettings(ctx, firstProject.ID())
	if err != nil || gotProject.Version != 2 || gotProject.AgentExecutionProfile != domain.AgentExecutionProfileIsolatedLocal {
		t.Fatalf("Project settings=%+v err=%v", gotProject, err)
	}
	gotTask, err := reopened.Reader().GetTaskExecutionSettings(ctx, firstTask.ID())
	if err != nil || gotTask.NativeCapabilitiesJSON != taskSettings.NativeCapabilitiesJSON || gotTask.ProjectVersion != 2 {
		t.Fatalf("Task settings=%+v err=%v", gotTask, err)
	}
}

func TestTaskExecutionSettingsEnforceOwnershipAndImmutability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "immutable.db")
	db, err := openLatestSQLiteTestStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	firstProject, _, firstTask := insertExecutionSettingsProject(t, ctx, db, "First", now)
	secondProject, _, _ := insertExecutionSettingsProject(t, ctx, db, "Second", now.Add(time.Second))
	settings := domain.TaskExecutionSettings{
		TaskID: firstTask.ID(), ProjectID: secondProject.ID(), AgentExecutionProfile: domain.AgentExecutionProfileTrustedLocal,
		EnvironmentSource: domain.ExecutionSettingsSourceTask, ModelSource: domain.ExecutionSettingsSourceDefault,
		NativeCapabilitiesJSON: `{}`, CreatedAt: now.Add(2 * time.Second),
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTaskExecutionSettings(ctx, settings)
	}); err == nil {
		t.Fatal("Task settings accepted a different owning Project")
	}
	settings.ProjectID = firstProject.ID()
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTaskExecutionSettings(ctx, settings)
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `UPDATE task_execution_settings SET environment_source='project' WHERE task_id=?`, firstTask.ID().String()); err == nil {
		t.Fatal("Task execution settings update succeeded")
	}
}

func insertExecutionSettingsProject(t *testing.T, ctx context.Context, db *sqlite.Store, name string, now time.Time) (domain.Project, domain.Room, domain.Task) {
	t.Helper()
	projectID, roomID := domain.NewProjectID(), domain.NewRoomID()
	project, err := domain.NewProject(domain.ProjectParams{ID: projectID, Name: name, DefaultRoomID: roomID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	room, err := domain.NewRoom(domain.RoomParams{
		ID: roomID, ProjectID: projectID, OwnershipKind: domain.RoomOwnershipProject, Name: name,
		WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), roomID, name+" task", "complete the task", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		return tx.InsertTask(ctx, task)
	}); err != nil {
		t.Fatal(err)
	}
	return project, room, task
}
