package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestProjectSettingsUpdateUsesActualProjectIdentityAndCAS(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "checkout")
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	projectID := opened.Project.Project.ID()
	first, err := fixture.svc.UpdateProjectSettings(ctx, app.UpdateProjectSettingsRequest{
		CommandMeta: meta("settings-create", "settings"), ProjectID: projectID,
		WritableFiles: []string{"README.md"}, WritableDirectories: []string{"docs"},
		VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"make", "test"}, WorkingDirectory: "."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID() != projectID || first.Version() != 1 {
		t.Fatalf("first=%+v", first)
	}
	if _, err := fixture.svc.GetProjectSettings(ctx, domain.NewProjectID()); !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("unknown Project error=%v", err)
	}
	second, err := fixture.svc.UpdateProjectSettings(ctx, app.UpdateProjectSettingsRequest{
		CommandMeta: meta("settings-update", "settings"), ProjectID: projectID, ExpectedVersion: first.Version(),
		WritableFiles: []string{"README.md", "docs/new.md"}, NoChecks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version() != 2 || !second.NoChecks() || !reflect.DeepEqual(second.WritableFiles(), []string{"README.md", "docs/new.md"}) {
		t.Fatalf("second=%+v", second)
	}
	_, err = fixture.svc.UpdateProjectSettings(ctx, app.UpdateProjectSettingsRequest{
		CommandMeta: meta("settings-stale", "settings"), ProjectID: projectID, ExpectedVersion: first.Version(),
		WritableFiles: []string{"README.md"}, NoChecks: true,
	})
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	loaded, err := fixture.svc.GetProjectSettings(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version() != second.Version() || !reflect.DeepEqual(loaded.WritableFiles(), second.WritableFiles()) {
		t.Fatalf("stale write changed settings: %+v", loaded)
	}
}

func TestArchivedProjectSettingsCannotChange(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "checkout")
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	archived, err := fixture.svc.ArchiveProject(ctx, app.ChangeProjectLifecycleRequest{
		CommandMeta: meta("settings-archive", "archive"), ProjectID: opened.Project.Project.ID(), ExpectedVersion: opened.Project.Project.Version(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.svc.UpdateProjectSettings(ctx, app.UpdateProjectSettingsRequest{
		CommandMeta: meta("settings-archived-update", "settings"), ProjectID: archived.Project.ID(),
		WritableFiles: []string{"README.md"}, NoChecks: true,
	})
	if !errors.Is(err, app.ErrInvalidCommand) {
		t.Fatalf("archived update error=%v", err)
	}
}
