package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestDefaultLocalConnectedUserTaskUsesTrackedRegularFilesAndProjectTestTarget(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "Makefile")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "add test target")

	defaults, err := defaultLocalConnectedUserTask(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.WritableFiles, []string{"Makefile", "README.md"}) {
		t.Fatalf("writable files=%v", defaults.WritableFiles)
	}
	if len(defaults.VerificationCommands) != 1 || !reflect.DeepEqual(defaults.VerificationCommands[0].Argv, []string{"make", "test"}) {
		t.Fatalf("verification=%v", defaults.VerificationCommands)
	}
	if len(defaults.Constraints) == 0 || len(defaults.OutOfScope) == 0 {
		t.Fatalf("incomplete defaults=%+v", defaults)
	}
}

func TestConfiguredProjectSettingsFreezeDirectoriesWorkingDirectoryAndNoChecks(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", ".keep"), []byte("docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "docs")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "add committed check directory")
	configured, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
		ProjectID: domain.NewProjectID(), WritableFiles: []string{"README.md", "docs/new.md"}, WritableDirectories: []string{"docs"},
		VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"go", "test", "./..."}, WorkingDirectory: "docs"}},
		UpdatedAt:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := defaultLocalConnectedUserTaskFromSettings(context.Background(), root, configured)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.WritableDirectories, []string{"docs"}) || len(defaults.VerificationCommands) != 1 || defaults.VerificationCommands[0].WorkingDirectory != "docs" || defaults.NoChecks {
		t.Fatalf("configured defaults=%+v", defaults)
	}

	withoutChecks, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
		ProjectID: domain.NewProjectID(), WritableFiles: []string{"README.md"}, NoChecks: true, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err = defaultLocalConnectedUserTaskFromSettings(context.Background(), root, withoutChecks)
	if err != nil {
		t.Fatal(err)
	}
	if !defaults.NoChecks || len(defaults.VerificationCommands) != 0 {
		t.Fatalf("no-check defaults=%+v", defaults)
	}
}

func TestDefaultLocalConnectedUserTaskRequiresSupportedTestCommand(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	if _, err := defaultLocalConnectedUserTask(context.Background(), root); err == nil {
		t.Fatal("repository without a supported test command was accepted")
	}
}

func TestTaskDefaultsUseCommittedTreeNotDirtyIndexOrTestConfig(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "Makefile")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "test config")
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("different:\n\t@false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("not in HEAD"), 0600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "staged.txt")
	defaults, err := defaultLocalConnectedUserTask(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.WritableFiles, []string{"Makefile", "README.md"}) {
		t.Fatal(defaults.WritableFiles)
	}
	if !reflect.DeepEqual(defaults.VerificationCommands[0].Argv, []string{"make", "test"}) {
		t.Fatal(defaults.VerificationCommands)
	}
	data, _ := os.ReadFile(filepath.Join(root, "Makefile"))
	if string(data) != "different:\n\t@false\n" {
		t.Fatal("dirty config mutated")
	}
}

func TestProjectSettingsRejectUncommittedCwdAndShadowedBaseSymlink(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "untracked"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings, err := domain.DetectProjectSettings(domain.ProjectSettingsParams{ProjectID: domain.NewProjectID(), WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"true"}, WorkingDirectory: "untracked"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProjectSettingsForRepository(context.Background(), root, settings); err == nil {
		t.Fatal("uncommitted cwd accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "linked")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "symlink base")
	if err := os.Remove(filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "linked", "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, params := range []domain.ProjectSettingsParams{
		{WritableFiles: []string{"linked/new.txt"}, NoChecks: true},
		{WritableDirectories: []string{"linked/subdir"}, NoChecks: true},
		{WritableFiles: []string{"README.md"}, VerificationCommands: []domain.ProjectVerificationCommand{{Argv: []string{"true"}, WorkingDirectory: "linked/subdir"}}},
	} {
		params.ProjectID = domain.NewProjectID()
		settings, err := domain.DetectProjectSettings(params)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateProjectSettingsForRepository(context.Background(), root, settings); err == nil {
			t.Fatalf("shadowed HEAD symlink accepted: %+v", params)
		}
	}
}

func TestBoundedGitMetadataRejectsOversizeOutput(t *testing.T) {
	root, _, revision := newTaskWorktreeTestRepository(t)
	if output, err := gitTargetOutputBounded(context.Background(), root, 4, "ls-tree", "-r", "-z", revision); !errors.Is(err, errGitMetadataLimit) || output != nil {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if output, err := gitTargetOutputBounded(context.Background(), root, 1024, "ls-tree", "-r", "-z", revision); err != nil || len(output) == 0 {
		t.Fatalf("bounded valid output=%q err=%v", output, err)
	}
}
