package gitsource_test

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/gitsource"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func newCommitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "chora@example.invalid")
	runGit(t, root, "config", "user.name", "Chora Test")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "file.txt")
	runGit(t, root, "commit", "-qm", "initial")
	return root
}

func TestInspectCleanGitWorktree(t *testing.T) {
	ctx := context.Background()
	root := newCommitRepo(t)
	head := runGit(t, root, "rev-parse", "HEAD")
	tree := runGit(t, root, "rev-parse", "HEAD^{tree}")
	inspection, err := gitsource.Default{}.Inspect(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.IsGit || inspection.IsBare || inspection.IsLinkedWorktree {
		t.Fatalf("flags = git=%t bare=%t linked=%t", inspection.IsGit, inspection.IsBare, inspection.IsLinkedWorktree)
	}
	if inspection.HeadCommit != head || inspection.RootTree != tree {
		t.Fatalf("head=%q tree=%q", inspection.HeadCommit, inspection.RootTree)
	}
	if inspection.Unborn || inspection.Branch != runGit(t, root, "branch", "--show-current") {
		t.Fatalf("branch=%q unborn=%t", inspection.Branch, inspection.Unborn)
	}
	if len(inspection.PhysicalIdentity) != 64 {
		t.Fatalf("physical identity length=%d", len(inspection.PhysicalIdentity))
	}
	if _, err := hex.DecodeString(inspection.PhysicalIdentity); err != nil || inspection.PhysicalIdentity != strings.ToLower(inspection.PhysicalIdentity) {
		t.Fatalf("physical identity=%q", inspection.PhysicalIdentity)
	}
	if inspection.Dirty {
		t.Fatal("clean worktree reported dirty")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.CanonicalPath != canonical {
		t.Fatalf("canonical=%q want %q", inspection.CanonicalPath, canonical)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if inspection.CommonGitDir != common {
		t.Fatalf("common Git dir=%q want %q", inspection.CommonGitDir, common)
	}
}

func TestInspectDirtyGitWorktree(t *testing.T) {
	ctx := context.Background()
	root := newCommitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := gitsource.Default{}.Inspect(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Dirty {
		t.Fatal("untracked file not reported dirty")
	}
}

func TestInspectUnbornRepositoryReturnsRecoveryEvidence(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	inspection, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.IsGit || !inspection.Unborn || inspection.Branch == "" {
		t.Fatalf("inspection = %+v", inspection)
	}
	if inspection.HeadCommit != "" || inspection.RootTree != "" || inspection.PhysicalIdentity == "" || inspection.CommonGitDir == "" {
		t.Fatalf("unborn repository evidence = %+v", inspection)
	}
}

func TestPhysicalIdentitySurvivesHeadChanges(t *testing.T) {
	root := newCommitRepo(t)
	first, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "second.txt")
	runGit(t, root, "commit", "-qm", "second")
	second, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.HeadCommit == second.HeadCommit {
		t.Fatal("test did not advance HEAD")
	}
	if first.PhysicalIdentity != second.PhysicalIdentity {
		t.Fatalf("physical identity changed with HEAD: %q -> %q", first.PhysicalIdentity, second.PhysicalIdentity)
	}
	if err := gitsource.ProveRevision(context.Background(), root, first.HeadCommit, first.RootTree); err != nil {
		t.Fatalf("historical admitted revision no longer proved after HEAD advanced: %v", err)
	}
	if err := gitsource.ProveRevision(context.Background(), root, first.HeadCommit, second.RootTree); !errors.Is(err, gitsource.ErrRevisionDrift) {
		t.Fatalf("mismatched historical tree error = %v, want ErrRevisionDrift", err)
	}
}

func TestPhysicalIdentityDetectsReplacedGitDirectory(t *testing.T) {
	root := newCommitRepo(t)
	first, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git-replaced")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q")
	second, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Unborn {
		t.Fatalf("replacement repository should be unborn: %+v", second)
	}
	if first.PhysicalIdentity == second.PhysicalIdentity {
		t.Fatalf("physical identity did not detect replaced .git: %q", first.PhysicalIdentity)
	}
}

func TestInspectCanonicalSymlinkAliasHasSameIdentity(t *testing.T) {
	root := newCommitRepo(t)
	alias := filepath.Join(t.TempDir(), "repository-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	direct, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	throughAlias, err := (gitsource.Default{}).Inspect(context.Background(), alias)
	if err != nil {
		t.Fatal(err)
	}
	if direct.CanonicalPath != throughAlias.CanonicalPath || direct.CommonGitDir != throughAlias.CommonGitDir || direct.PhysicalIdentity != throughAlias.PhysicalIdentity {
		t.Fatalf("alias identity drift: direct=%+v alias=%+v", direct, throughAlias)
	}
}

func TestInspectDoesNotReadUnchangedLargeBinaryBlob(t *testing.T) {
	root := newCommitRepo(t)
	binary := make([]byte, 2*1024*1024)
	for index := range binary {
		binary[index] = byte(index)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), binary, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "large.bin")
	runGit(t, root, "commit", "-qm", "large binary")
	inspection, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Dirty || inspection.HeadCommit == "" || inspection.RootTree == "" {
		t.Fatalf("large unchanged blob affected metadata inspection: %+v", inspection)
	}
}

func TestInspectBareRepository(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "bare.git")
	if err := os.MkdirAll(bare, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, t.TempDir(), "init", "-q", "--bare", bare)
	if _, err := (gitsource.Default{}).Inspect(ctx, bare); !errors.Is(err, gitsource.ErrBare) {
		t.Fatalf("Inspect() error = %v, want ErrBare", err)
	}
}

func TestInspectLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	root := newCommitRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, root, "worktree", "add", "-q", linked, "HEAD")
	if _, err := (gitsource.Default{}).Inspect(ctx, linked); !errors.Is(err, gitsource.ErrLinkedWorktree) {
		t.Fatalf("Inspect() error = %v, want ErrLinkedWorktree", err)
	}
}

func TestInspectNotGit(t *testing.T) {
	ctx := context.Background()
	if _, err := (gitsource.Default{}).Inspect(ctx, t.TempDir()); !errors.Is(err, gitsource.ErrNotGit) {
		t.Fatalf("Inspect() error = %v, want ErrNotGit", err)
	}
}

func TestInspectMissingPathWrapsOSError(t *testing.T) {
	ctx := context.Background()
	_, err := gitsource.Default{}.Inspect(ctx, filepath.Join(t.TempDir(), "does-not-exist"))
	var pathErr *gitsource.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("Inspect() error = %v, want *gitsource.PathError", err)
	}
}

func TestCloneAndReinspect(t *testing.T) {
	ctx := context.Background()
	source := newCommitRepo(t)
	sourceHead := runGit(t, source, "rev-parse", "HEAD")
	dest := filepath.Join(t.TempDir(), "cloned")
	inspection, err := gitsource.Default{}.Clone(ctx, source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.IsGit || inspection.Dirty {
		t.Fatalf("cloned flags git=%t dirty=%t", inspection.IsGit, inspection.Dirty)
	}
	if inspection.HeadCommit != sourceHead {
		t.Fatalf("cloned head=%q want %q", inspection.HeadCommit, sourceHead)
	}
	if filepath.Clean(inspection.RemoteURL) != filepath.Clean(source) {
		t.Fatalf("remote=%q want %q", inspection.RemoteURL, source)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("clone did not create destination: %v", err)
	}
}

func TestCloneFailureIsGeneric(t *testing.T) {
	ctx := context.Background()
	_, err := gitsource.Default{}.Clone(ctx, "https://user:secret@example.invalid/private.git", filepath.Join(t.TempDir(), "dest"))
	if !errors.Is(err, gitsource.ErrCloneFailed) {
		t.Fatalf("Clone() error = %v, want ErrCloneFailed", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("clone error echoed credentials: %v", err)
	}
}

func TestCloneFailureRemovesPartialDestination(t *testing.T) {
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "dest")
	_, err := gitsource.Default{}.Clone(ctx, "https://example.invalid/not-a-repo.git", dest)
	if !errors.Is(err, gitsource.ErrCloneFailed) {
		t.Fatalf("Clone() error = %v, want ErrCloneFailed", err)
	}
	if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial destination not removed: %v", statErr)
	}
}

func TestCloneExistingDestinationRejected(t *testing.T) {
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitsource.Default{}).Clone(ctx, "https://example.invalid/any.git", dest); !errors.Is(err, gitsource.ErrCloneFailed) {
		t.Fatalf("Clone() error = %v, want ErrCloneFailed", err)
	}
}
