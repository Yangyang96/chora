package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestProjectSettingsPersistCASAndRestart(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	room, _ := insertRoomAndBinding(t, db, seeded.now, t.TempDir())
	settings, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
		ProjectID: room.ProjectID(), WritableFiles: []string{"README.md", "docs/new file.md"},
		WritableDirectories:  []string{"internal/generated"},
		VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"go", "test", "./..."}, WorkingDirectory: "."}},
		UpdatedAt:            seeded.now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectSettingsCAS(ctx, 0, settings)
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := settings.Update(domain.ProjectSettingsParams{
		ProjectID: room.ProjectID(), WritableFiles: []string{"README.md"}, NoChecks: true,
		UpdatedAt: seeded.now.Add(2 * time.Second),
	}, settings.Version())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectSettingsCAS(ctx, 0, settings)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale insert error=%v", err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveProjectSettingsCAS(ctx, settings.Version(), stale)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Reader().GetProjectSettings(ctx, room.ProjectID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version() != 2 || !loaded.NoChecks() || !reflect.DeepEqual(loaded.WritableFiles(), []string{"README.md"}) || len(loaded.VerificationCommands()) != 0 {
		t.Fatalf("restarted settings=%+v", loaded)
	}
}

func TestProjectSettingsRejectUnsafeAndAmbiguousAuthority(t *testing.T) {
	projectID := domain.NewProjectID()
	now := time.Now().UTC()
	for _, test := range []struct {
		name   string
		params domain.ProjectSettingsParams
	}{
		{name: "repository root", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableDirectories: []string{"."}, NoChecks: true, UpdatedAt: now}},
		{name: "git metadata", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{".git/config"}, NoChecks: true, UpdatedAt: now}},
		{name: "path escape", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"../outside"}, NoChecks: true, UpdatedAt: now}},
		{name: "missing check choice", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, UpdatedAt: now}},
		{name: "checks plus no checks", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"make", "test"}}}, NoChecks: true, UpdatedAt: now}},
		{name: "absolute executable", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"/bin/sh"}}}, UpdatedAt: now}},
		{name: "git executable", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{".git/hooks/check"}}}, UpdatedAt: now}},
		{name: "executable escape", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"scripts/../check"}}}, UpdatedAt: now}},
		{name: "cwd escape", params: domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"go", "test"}, WorkingDirectory: "../other"}}, UpdatedAt: now}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := domain.NewProjectSettings(test.params); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
