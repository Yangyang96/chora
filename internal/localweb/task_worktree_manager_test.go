package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestTaskWorktreeManagerPlansDistinctSafeBindingsAndEnsuresIdempotently(t *testing.T) {
	manager, anchor, parent, revision := newTaskWorktreeTestManager(t)
	branchesBefore := runTaskWorktreeGitTest(t, anchor, "branch", "--format=%(refname)")
	createdAt := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)
	firstID := domain.NewTaskID()
	first, err := manager.Plan(firstID, " Fix Apply / Keep INDEX clean! ", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "chora-" + firstID.String() + "-"
	if first.RelativeLocator() != wantPrefix+"fix-apply-keep-index-clean" || first.PinnedBaseRevision() != revision ||
		first.ConfiguredRootFingerprint() == ([32]byte{}) || strings.Contains(first.RelativeLocator(), parent) {
		t.Fatalf("binding locator=%q revision=%q fingerprint=%x", first.RelativeLocator(), first.PinnedBaseRevision(), first.ConfiguredRootFingerprint())
	}
	second, err := manager.Plan(domain.NewTaskID(), "修正应用流程", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if second.RelativeLocator() == first.RelativeLocator() || !strings.HasSuffix(second.RelativeLocator(), "-task") {
		t.Fatalf("second locator=%q", second.RelativeLocator())
	}
	if err := manager.Ensure(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// A crash can leave the durable binding provisioning after Git succeeded.
	// Replaying Ensure must prove the existing exact worktree without mutation.
	if err := manager.Ensure(context.Background(), first); err != nil {
		t.Fatalf("idempotent provisioning ensure: %v", err)
	}
	ready, err := first.Ready(createdAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.ResolveReady(context.Background(), ready)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(filepath.Dir(anchor), filepath.FromSlash(first.RelativeLocator()))
	if resolved != wantPath {
		t.Fatalf("resolved=%q want=%q", resolved, wantPath)
	}
	if head := strings.TrimSpace(runTaskWorktreeGitTest(t, resolved, "rev-parse", "HEAD")); head != revision {
		t.Fatalf("HEAD=%q want=%q", head, revision)
	}
	if branch := strings.TrimSpace(runTaskWorktreeGitTest(t, resolved, "rev-parse", "--abbrev-ref", "HEAD")); branch != "HEAD" {
		t.Fatalf("worktree is not detached: %q", branch)
	}
	if branchesAfter := runTaskWorktreeGitTest(t, anchor, "branch", "--format=%(refname)"); branchesAfter != branchesBefore {
		t.Fatalf("Ensure created or changed a branch\nbefore=%q\nafter=%q", branchesBefore, branchesAfter)
	}
	if err := manager.Ensure(context.Background(), ready); err != nil {
		t.Fatalf("ready ensure: %v", err)
	}
}

func TestTaskWorktreeManagerRejectsForeignDirectoryAndSymlinkWithoutReplacement(t *testing.T) {
	manager, _, _, _ := newTaskWorktreeTestManager(t)
	createdAt := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)

	t.Run("foreign directory", func(t *testing.T) {
		binding, err := manager.Plan(domain.NewTaskID(), "foreign", createdAt)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(path, "keep.txt")
		if err := os.WriteFile(marker, []byte("foreign\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := manager.Ensure(context.Background(), binding); !errors.Is(err, errTaskWorktreeNotProven) {
			t.Fatalf("err=%v", err)
		}
		if got, err := os.ReadFile(marker); err != nil || string(got) != "foreign\n" {
			t.Fatalf("foreign state changed: %q err=%v", got, err)
		}
	})

	t.Run("foreign Git repository", func(t *testing.T) {
		binding, err := manager.Plan(domain.NewTaskID(), "foreign git", createdAt)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "foreign.txt"), []byte("foreign git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, path, "init")
		runTaskWorktreeGitTest(t, path, "add", "foreign.txt")
		runTaskWorktreeGitTest(t, path, "-c", "user.name=Foreign Test", "-c", "user.email=chora@example.test", "commit", "-m", "foreign")
		foreignHead := strings.TrimSpace(runTaskWorktreeGitTest(t, path, "rev-parse", "HEAD"))
		if err := manager.Ensure(context.Background(), binding); !errors.Is(err, errTaskWorktreeNotProven) {
			t.Fatalf("err=%v", err)
		}
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, path, "rev-parse", "HEAD")); got != foreignHead {
			t.Fatalf("foreign repository changed HEAD=%q want=%q", got, foreignHead)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		binding, err := manager.Plan(domain.NewTaskID(), "symlink", createdAt)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		if err := manager.Ensure(context.Background(), binding); !errors.Is(err, errTaskWorktreeNotProven) {
			t.Fatalf("err=%v", err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink was replaced: info=%v err=%v", info, err)
		}
	})
}

func TestTaskWorktreeManagerFailsClosedOnReadyMissingAndDriftedState(t *testing.T) {
	manager, anchor, _, _ := newTaskWorktreeTestManager(t)
	createdAt := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)

	t.Run("missing", func(t *testing.T) {
		binding, err := manager.Plan(domain.NewTaskID(), "missing", createdAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Ensure(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
		ready, err := binding.Ready(createdAt.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
		runTaskWorktreeGitTest(t, anchor, "worktree", "remove", path)
		if err := manager.Ensure(context.Background(), ready); !errors.Is(err, errTaskWorktreeNotProven) {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing ready worktree was recreated: %v", err)
		}
	})

	t.Run("wrong HEAD", func(t *testing.T) {
		binding, err := manager.Plan(domain.NewTaskID(), "drift", createdAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Ensure(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
		ready, err := binding.Ready(createdAt.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
		if err := os.WriteFile(filepath.Join(anchor, "second.txt"), []byte("second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, anchor, "add", "second.txt")
		runTaskWorktreeGitTest(t, anchor, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "second")
		secondRevision := strings.TrimSpace(runTaskWorktreeGitTest(t, anchor, "rev-parse", "HEAD"))
		runTaskWorktreeGitTest(t, path, "reset", "--hard", secondRevision)
		if _, err := manager.ResolveReady(context.Background(), ready); !errors.Is(err, errTaskWorktreeNotProven) {
			t.Fatalf("err=%v", err)
		}
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, path, "rev-parse", "HEAD")); got != secondRevision {
			t.Fatalf("manager modified drifted HEAD=%q", got)
		}
	})
}

func TestTaskWorktreeManagerRejectsMismatchedDurableAuthority(t *testing.T) {
	manager, _, _, _ := newTaskWorktreeTestManager(t)
	createdAt := time.Date(2026, 8, 25, 7, 0, 0, 0, time.UTC)
	taskID := domain.NewTaskID()
	binding, err := manager.Plan(taskID, "authority", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoot, err := domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: binding.RepositoryIdentity(), PinnedBaseRevision: binding.PinnedBaseRevision(),
		RelativeLocator: binding.RelativeLocator(), ConfiguredRootFingerprint: sha256.Sum256([]byte("wrong root")), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background(), wrongRoot); !errors.Is(err, errTaskWorktreeNotProven) {
		t.Fatalf("err=%v", err)
	}
	path := filepath.Join(filepath.Dir(manager.repositoryAnchor), filepath.FromSlash(binding.RelativeLocator()))
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched binding created state: %v", err)
	}
}

func TestTaskWorktreeManagerValidatesConfigurationAndBoundsSlug(t *testing.T) {
	anchor, parent, revision := newTaskWorktreeTestRepository(t)
	repository := speccoding.RepositoryIdentity{Name: "chora", SourceRevision: revision}
	if _, err := newTaskWorktreeManager(anchor+string(filepath.Separator), repository); err == nil {
		t.Fatal("unclean anchor accepted")
	}
	manager, err := newTaskWorktreeManager(anchor, repository)
	if err != nil {
		t.Fatal(err)
	}
	if manager.repositoryParent != parent {
		t.Fatalf("repository parent=%q want=%q", manager.repositoryParent, parent)
	}
	binding, err := manager.Plan(domain.NewTaskID(), strings.Repeat("Long Topic ", 40), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	basename := filepath.Base(binding.RelativeLocator())
	slug := strings.TrimPrefix(basename, "chora-"+binding.TaskID().String()+"-")
	if len(slug) == 0 || len(slug) > taskWorktreeTopicSlugLimit || strings.HasSuffix(slug, "-") {
		t.Fatalf("slug=%q len=%d", slug, len(slug))
	}
}

func TestTaskWorktreeManagerModeSeparatesSourceCheckoutGitAuthorityFromInstalledApplyTarget(t *testing.T) {
	anchor, _, revision := newTaskWorktreeTestRepository(t)
	repository := speccoding.RepositoryIdentity{Name: "chora", SourceRevision: revision}
	installedValidationErr := errors.New("installed control-plane files are absent")
	validationCalls := 0
	validateInstalled := func(string) error {
		validationCalls++
		return installedValidationErr
	}

	manager, err := newTaskWorktreeManagerForMode(anchor, repository, true, validateInstalled)
	if err != nil {
		t.Fatalf("clean SourceCheckout Git target rejected: %v", err)
	}
	if manager == nil || validationCalls != 0 {
		t.Fatalf("SourceCheckout manager=%v installed validation calls=%d", manager != nil, validationCalls)
	}
	if _, err := manager.Plan(domain.NewTaskID(), "source checkout", time.Now().UTC()); err != nil {
		t.Fatalf("SourceCheckout manager cannot plan a Task worktree: %v", err)
	}

	if manager, err := newTaskWorktreeManagerForMode(anchor, repository, false, validateInstalled); manager != nil || !errors.Is(err, installedValidationErr) {
		t.Fatalf("installed apply-target failure was bypassed: manager=%v err=%v", manager != nil, err)
	}
	if validationCalls != 1 {
		t.Fatalf("installed validation calls=%d want=1", validationCalls)
	}

	invalidAnchor := filepath.Join(filepath.Dir(anchor), "not-a-git-worktree")
	if err := os.Mkdir(invalidAnchor, 0o700); err != nil {
		t.Fatal(err)
	}
	if manager, err := newTaskWorktreeManagerForMode(invalidAnchor, repository, true, validateInstalled); manager != nil || err == nil {
		t.Fatalf("SourceCheckout accepted invalid Git authority: manager=%v err=%v", manager != nil, err)
	}
	if validationCalls != 1 {
		t.Fatalf("invalid SourceCheckout consulted installed validator: calls=%d", validationCalls)
	}
}

func TestTaskWorktreeManagerMovesLegacyNestedWorktreeToDirectSibling(t *testing.T) {
	manager, anchor, parent, revision := newTaskWorktreeTestManager(t)
	createdAt := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	taskID := domain.NewTaskID()
	locator := "chora-" + taskID.String() + "-accepted-change"
	binding, err := domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: revision,
		RelativeLocator: locator, ConfiguredRootFingerprint: manager.legacyFingerprint, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := binding.Ready(createdAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(parent, "worktrees")
	if err := os.Mkdir(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyRoot, taskID.String()+"-accepted-change")
	runTaskWorktreeGitTest(t, anchor, "worktree", "add", "--detach", legacyPath, revision)
	if err := os.WriteFile(filepath.Join(legacyPath, "README.md"), []byte("accepted dirty change\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := manager.Ensure(context.Background(), ready); err != nil {
		t.Fatal(err)
	}
	directPath := filepath.Join(parent, locator)
	if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy nested worktree still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(directPath, "README.md")); err != nil || string(got) != "accepted dirty change\n" {
		t.Fatalf("dirty accepted bytes were not preserved: %q err=%v", got, err)
	}
	if status := runTaskWorktreeGitTest(t, directPath, "status", "--short"); !strings.Contains(status, "README.md") {
		t.Fatalf("moved worktree lost dirty status: %q", status)
	}
}

func newTaskWorktreeTestManager(t *testing.T) (*taskWorktreeManager, string, string, string) {
	t.Helper()
	anchor, parent, revision := newTaskWorktreeTestRepository(t)
	manager, err := newTaskWorktreeManager(anchor, speccoding.RepositoryIdentity{Name: "chora", SourceRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	return manager, anchor, parent, revision
}

func newTaskWorktreeTestRepository(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	anchor := filepath.Join(base, "repository")
	if err := os.Mkdir(anchor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, anchor, "init")
	runTaskWorktreeGitTest(t, anchor, "add", "README.md")
	runTaskWorktreeGitTest(t, anchor, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "initial")
	revision := strings.TrimSpace(runTaskWorktreeGitTest(t, anchor, "rev-parse", "HEAD"))
	return anchor, base, revision
}

func runTaskWorktreeGitTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}
