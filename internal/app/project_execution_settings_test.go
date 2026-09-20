package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestProjectExecutionSettingsDefaultsUpdateReplayAndCAS(t *testing.T) {
	fixture := newProjectFixture(t)
	repo := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.registerRepo(repo, false)
	projectID := fixture.openResult(repo).Project.Project.ID()

	initial, err := fixture.svc.GetProjectExecutionSettings(context.Background(), projectID)
	if err != nil || initial.Version != 0 || initial.AgentExecutionProfile != domain.AgentExecutionProfileIsolatedLocal || initial.Model != nil {
		t.Fatalf("default=%+v err=%v", initial, err)
	}
	selected := &domain.ModelIdentity{Provider: "openai", ModelID: "gpt-5"}
	resolver := func(_ context.Context, _ domain.AgentExecutionProfile, selected *domain.ModelIdentity) (domain.ModelBinding, error) {
		catalog, err := domain.NewModelCatalog("pi", "local", "1", []domain.ModelIdentity{*selected})
		if err != nil {
			return domain.ModelBinding{}, err
		}
		return domain.NewModelBinding(catalog, *selected, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	}
	request := app.UpdateProjectExecutionSettingsRequest{CommandMeta: meta("execution-settings", "execution-settings"), ProjectID: projectID, AgentExecutionProfile: domain.AgentExecutionProfileTrustedLocal, Model: selected, ResolveExecutionModel: resolver}
	updated, err := fixture.svc.UpdateProjectExecutionSettings(context.Background(), request)
	if err != nil || updated.Version != 1 || updated.AgentExecutionProfile != domain.AgentExecutionProfileTrustedLocal || updated.Model == nil || *updated.Model != *selected {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	request.ResolveExecutionModel = nil
	replayed, err := fixture.svc.UpdateProjectExecutionSettings(context.Background(), request)
	if err != nil || replayed.ProjectID != updated.ProjectID || replayed.Version != updated.Version || replayed.AgentExecutionProfile != updated.AgentExecutionProfile || replayed.Model == nil || *replayed.Model != *updated.Model || !replayed.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	_, err = fixture.svc.UpdateProjectExecutionSettings(context.Background(), app.UpdateProjectExecutionSettingsRequest{CommandMeta: meta("execution-settings-stale", "execution-settings"), ProjectID: projectID, AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal})
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	_, err = fixture.svc.UpdateProjectExecutionSettings(context.Background(), app.UpdateProjectExecutionSettingsRequest{CommandMeta: meta("explicit-model-no-resolver", "execution-settings"), ProjectID: projectID, ExpectedVersion: 1, AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal, Model: selected})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("missing model resolver error=%v", err)
	}
}

func TestTaskCreationFreezesInheritedAndExplicitRuntimeDefaultSettings(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	projectID := opened.Project.Project.ID()
	model := &domain.ModelIdentity{Provider: "openai", ModelID: "gpt-5"}
	resolver := func(_ context.Context, profile domain.AgentExecutionProfile, selected *domain.ModelIdentity) (domain.ModelBinding, error) {
		if profile != domain.AgentExecutionProfileIsolatedLocal {
			t.Fatalf("profile=%s", profile)
		}
		if selected == nil {
			return domain.ModelBinding{}, nil
		}
		catalog, err := domain.NewModelCatalog("pi", "isolated", "1", []domain.ModelIdentity{*selected})
		if err != nil {
			return domain.ModelBinding{}, err
		}
		return domain.NewModelBinding(catalog, *selected, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	}
	projectSettings, err := fixture.svc.UpdateProjectExecutionSettings(ctx, app.UpdateProjectExecutionSettingsRequest{
		CommandMeta: meta("task-defaults", "task-defaults"), ProjectID: projectID,
		AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal, Model: model, ResolveExecutionModel: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := newRealSpecCodingTestEnvelope(repo)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{
		Store: fixture.db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
		SpecCodingEnvelopeResolver: fixedSpecCodingEnvelopeResolver{envelope}, DataRoot: t.TempDir(),
	})
	create := func(key string, settings *app.ExecutionSettingsSelection) app.CreateTaskResult {
		revisions, err := fixture.db.Reader().ListRoomRevisions(ctx, opened.Project.Room.ID())
		if err != nil || len(revisions) == 0 {
			t.Fatalf("revisions=%d err=%v", len(revisions), err)
		}
		result, err := service.CreateTask(ctx, app.CreateTaskRequest{
			CommandMeta: meta(key, key), RoomID: opened.Project.Room.ID(), Title: key,
			ExecutionProfile: app.TaskExecutionProfileRealSpecCoding, RevisionIDs: []domain.ContextRevisionID{revisions[0].ID()},
			ExecutionSettings: settings, ResolveExecutionModel: resolver,
			RealSpecCoding: &app.RealSpecCodingInput{Requirement: key, Constraints: []string{"Preserve existing behavior."}, OutOfScope: []string{"Unrelated changes."}, WritableFiles: []string{"README.md"}, Criteria: []app.RealSpecCodingCriterion{{Title: "done", Description: "done", VerificationCommandIndexes: []int{0}}}, VerificationCommands: []app.RealSpecCodingVerificationCommand{{Argv: []string{"go", "test", "./..."}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := create("Implement inherited execution settings", &app.ExecutionSettingsSelection{ProjectVersion: projectSettings.Version})
	frozen, err := fixture.db.Reader().GetTaskExecutionSettings(ctx, first.Task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if frozen.ProjectVersion != 1 || frozen.EnvironmentSource != domain.ExecutionSettingsSourceProject || frozen.ModelSource != domain.ExecutionSettingsSourceProject || !frozen.ModelBinding.Configured() {
		t.Fatalf("frozen=%+v", frozen)
	}
	if _, err = fixture.svc.UpdateProjectExecutionSettings(ctx, app.UpdateProjectExecutionSettingsRequest{
		CommandMeta: meta("change-defaults", "change-defaults"), ProjectID: projectID, ExpectedVersion: 1,
		AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal,
	}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := fixture.db.Reader().GetTaskExecutionSettings(ctx, first.Task.ID())
	if err != nil || unchanged.ModelBinding.JSON() != frozen.ModelBinding.JSON() || unchanged.ProjectVersion != frozen.ProjectVersion {
		t.Fatalf("changed frozen settings=%+v err=%v", unchanged, err)
	}
	replay := create("Implement inherited execution settings", &app.ExecutionSettingsSelection{ProjectVersion: projectSettings.Version})
	if !replay.Replayed || replay.Task.ID() != first.Task.ID() || replay.Task.ModelBinding().JSON() != first.Task.ModelBinding().JSON() {
		t.Fatalf("create replay=%+v", replay)
	}
	_, err = service.CreateTask(ctx, app.CreateTaskRequest{
		CommandMeta: meta("stale-task-settings", "stale-task-settings"), RoomID: opened.Project.Room.ID(), Title: "Reject stale execution settings",
		ExecutionProfile: app.TaskExecutionProfileRealSpecCoding, RevisionIDs: []domain.ContextRevisionID{func() domain.ContextRevisionID {
			revisions, _ := fixture.db.Reader().ListRoomRevisions(ctx, opened.Project.Room.ID())
			return revisions[0].ID()
		}()},
		ExecutionSettings: &app.ExecutionSettingsSelection{ProjectVersion: 1}, ResolveExecutionModel: resolver,
		RealSpecCoding: &app.RealSpecCodingInput{Requirement: "Reject stale execution settings", Constraints: []string{"Preserve existing behavior."}, OutOfScope: []string{"Unrelated changes."}, WritableFiles: []string{"README.md"}, Criteria: []app.RealSpecCodingCriterion{{Title: "done", Description: "done", VerificationCommandIndexes: []int{0}}}, VerificationCommands: []app.RealSpecCodingVerificationCommand{{Argv: []string{"go", "test", "./..."}}}},
	})
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale Task settings error=%v", err)
	}
	second := create("Use the runtime default model", &app.ExecutionSettingsSelection{ProjectVersion: 2, Model: []byte("null")})
	explicitDefault, err := fixture.db.Reader().GetTaskExecutionSettings(ctx, second.Task.ID())
	if err != nil || explicitDefault.ModelSource != domain.ExecutionSettingsSourceTask || explicitDefault.ModelBinding.Configured() {
		t.Fatalf("explicit runtime default=%+v err=%v", explicitDefault, err)
	}
}
