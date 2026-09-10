package localweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
)

func TestResourcePatchTargetAppliesAndReconcilesWithoutTouchingIndexOrUnrelatedDirt(t *testing.T) {
	fixture := newResourcePatchTargetFixture(t)
	writeResourcePatchTargetFile(t, fixture.root, "staged.txt", "staged and unrelated\n")
	runTaskWorktreeGitTest(t, fixture.root, "add", "staged.txt")
	writeResourcePatchTargetFile(t, fixture.root, "untracked.txt", "untracked and unrelated\n")
	indexBefore, err := resourceGitIndexDigest(context.Background(), fixture.root, fixture.resource.CommonGitDir)
	if err != nil {
		t.Fatal(err)
	}

	inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
	if err != nil || !inspection.Applicable || inspection.AlreadyApplied || inspection.StateDigest == ([32]byte{}) || inspection.BaseRevision != fixture.resource.BaseCommit {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	evidence, err := fixture.target.Apply(context.Background(), fixture.resource, fixture.request, inspection)
	if err != nil || evidence.PostStateDigest == ([32]byte{}) || evidence.PostStateDigest == inspection.StateDigest {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.root, "README.md"))); got != "applied\n" {
		t.Fatalf("README=%q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.root, "staged.txt"))); got != "staged and unrelated\n" {
		t.Fatalf("staged unrelated=%q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.root, "untracked.txt"))); got != "untracked and unrelated\n" {
		t.Fatalf("untracked unrelated=%q", got)
	}
	indexAfter, err := resourceGitIndexDigest(context.Background(), fixture.root, fixture.resource.CommonGitDir)
	if err != nil || indexAfter != indexBefore {
		t.Fatalf("index changed: before=%x after=%x err=%v", indexBefore, indexAfter, err)
	}

	reconcile := fixture.request
	reconcile.PriorPreStateDigest = inspection.StateDigest
	reconciled, err := fixture.target.Inspect(context.Background(), fixture.resource, reconcile)
	if err != nil || !reconciled.AlreadyApplied || reconciled.Applicable || reconciled.StateDigest != evidence.PostStateDigest {
		t.Fatalf("reconciled=%+v err=%v", reconciled, err)
	}
}

func TestResourcePatchTargetProvesMultiFileAddModifyDeletePostState(t *testing.T) {
	fixture := newResourcePatchTargetFixture(t)
	writeResourcePatchTargetFile(t, fixture.root, "delete.txt", "remove me\n")
	runTaskWorktreeGitTest(t, fixture.root, "add", "delete.txt")
	runTaskWorktreeGitTest(t, fixture.root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "multi-file base")
	inspection, err := (gitsource.Default{}).Inspect(context.Background(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.resource.BaseCommit, fixture.resource.BaseTree = inspection.HeadCommit, inspection.RootTree
	fixture.resource.BaseRef, err = currentTaskRef(context.Background(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-initial\n+applied\n" +
		"diff --git a/delete.txt b/delete.txt\ndeleted file mode 100644\n--- a/delete.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-remove me\n" +
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new file\n")
	fixture.request.Raw = patch
	fixture.request.PatchDigest = sha256.Sum256(patch)
	fixture.request.AffectedPaths = []string{"README.md", "delete.txt", "new.txt"}

	preflight, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
	if err != nil || !preflight.Applicable {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	evidence, err := fixture.target.Apply(context.Background(), fixture.resource, fixture.request, preflight)
	if err != nil || evidence.PostStateDigest == ([32]byte{}) {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.root, "README.md"))); got != "applied\n" {
		t.Fatalf("README=%q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.root, "new.txt"))); got != "new file\n" {
		t.Fatalf("new.txt=%q", got)
	}
	if _, err := os.Lstat(filepath.Join(fixture.root, "delete.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete.txt still exists: %v", err)
	}
}

func TestResourcePatchTargetRejectsFrozenBaseAndBranchDrift(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		writeResourcePatchTargetFile(t, fixture.root, "later.txt", "later commit\n")
		runTaskWorktreeGitTest(t, fixture.root, "add", "later.txt")
		runTaskWorktreeGitTest(t, fixture.root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "later")
		if inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request); !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
	t.Run("branch", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		runTaskWorktreeGitTest(t, fixture.root, "checkout", "-b", "other-branch")
		if inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request); !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
	t.Run("physical identity", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		drifted := fixture.resource
		drifted.PhysicalIdentity = strings.Repeat("f", 64)
		if inspection, err := fixture.target.Inspect(context.Background(), drifted, fixture.request); !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
}

func TestResourcePatchTargetRejectsDirtyAffectedWorktreeAndIndex(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		writeResourcePatchTargetFile(t, fixture.root, "README.md", "initial\nhuman edit that would not break apply context\n")
		before := string(mustReadFile(t, filepath.Join(fixture.root, "README.md")))
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
		if got := string(mustReadFile(t, filepath.Join(fixture.root, "README.md"))); got != before {
			t.Fatalf("dirty affected file changed: %q", got)
		}
	})
	t.Run("index", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		writeResourcePatchTargetFile(t, fixture.root, "README.md", "staged human edit\n")
		runTaskWorktreeGitTest(t, fixture.root, "add", "README.md")
		writeResourcePatchTargetFile(t, fixture.root, "README.md", "initial\n")
		indexBefore, err := resourceGitIndexDigest(context.Background(), fixture.root, fixture.resource.CommonGitDir)
		if err != nil {
			t.Fatal(err)
		}
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
		indexAfter, digestErr := resourceGitIndexDigest(context.Background(), fixture.root, fixture.resource.CommonGitDir)
		if digestErr != nil || indexAfter != indexBefore {
			t.Fatalf("index changed: before=%x after=%x err=%v", indexBefore, indexAfter, digestErr)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, "README.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("unrelated.txt", filepath.Join(fixture.root, "README.md")); err != nil {
			t.Fatal(err)
		}
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if !errors.Is(err, app.ErrPatchTargetConflict) || inspection.Applicable || inspection.AlreadyApplied {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	})
}

func TestResourcePatchTargetDistinguishesStalePreflightFromUncertainPriorWrite(t *testing.T) {
	t.Run("stale preflight proves no target write", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		writeResourcePatchTargetFile(t, fixture.root, "README.md", "human won\n")
		if _, err := fixture.target.Apply(context.Background(), fixture.resource, fixture.request, inspection); !errors.Is(err, app.ErrPatchTargetConflict) || errors.Is(err, app.ErrPatchTargetRecovery) {
			t.Fatalf("stale Apply err=%v", err)
		}
		if got := string(mustReadFile(t, filepath.Join(fixture.root, "README.md"))); got != "human won\n" {
			t.Fatalf("stale Apply changed target=%q", got)
		}
	})

	t.Run("post-write plus external edit is uncertain", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.target.Apply(context.Background(), fixture.resource, fixture.request, inspection); err != nil {
			t.Fatal(err)
		}
		writeResourcePatchTargetFile(t, fixture.root, "README.md", "applied\nhuman after write\n")
		reconcile := fixture.request
		reconcile.PriorPreStateDigest = inspection.StateDigest
		got, err := fixture.target.Inspect(context.Background(), fixture.resource, reconcile)
		if !errors.Is(err, app.ErrPatchTargetRecovery) || got.AlreadyApplied || got.Applicable {
			t.Fatalf("reconcile=%+v err=%v", got, err)
		}
	})

	t.Run("unknown prior pre-state is uncertain", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		reconcile := fixture.request
		reconcile.PriorPreStateDigest = sha256.Sum256([]byte("not the recorded pre-state"))
		got, err := fixture.target.Inspect(context.Background(), fixture.resource, reconcile)
		if !errors.Is(err, app.ErrPatchTargetRecovery) || got.AlreadyApplied || got.Applicable {
			t.Fatalf("reconcile=%+v err=%v", got, err)
		}
	})

	t.Run("post-write index drift is uncertain", func(t *testing.T) {
		fixture := newResourcePatchTargetFixture(t)
		inspection, err := fixture.target.Inspect(context.Background(), fixture.resource, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.target.Apply(context.Background(), fixture.resource, fixture.request, inspection); err != nil {
			t.Fatal(err)
		}
		writeResourcePatchTargetFile(t, fixture.root, "staged-after-write.txt", "external staging\n")
		runTaskWorktreeGitTest(t, fixture.root, "add", "staged-after-write.txt")
		reconcile := fixture.request
		reconcile.PriorPreStateDigest = inspection.StateDigest
		got, err := fixture.target.Inspect(context.Background(), fixture.resource, reconcile)
		if !errors.Is(err, app.ErrPatchTargetRecovery) || got.AlreadyApplied || got.Applicable {
			t.Fatalf("reconcile=%+v err=%v", got, err)
		}
	})
}

type resourcePatchTargetFixture struct {
	root     string
	resource domain.TaskRepositoryResource
	request  app.PatchTargetRequest
	target   app.ResourcePatchApplicationTarget
}

func newResourcePatchTargetFixture(t *testing.T) resourcePatchTargetFixture {
	t.Helper()
	root, _, _ := newTaskWorktreeTestRepository(t)
	inspection, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := currentTaskRef(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	resource := domain.TaskRepositoryResource{
		RepoID: domain.NewRepositoryID().String(), Name: "resource apply fixture", Checkout: inspection.CanonicalPath,
		CommonGitDir: inspection.CommonGitDir, PhysicalIdentity: inspection.PhysicalIdentity, Role: "write",
		BaseCommit: inspection.HeadCommit, BaseTree: inspection.RootTree, BaseRef: ref, AssociationVersion: 1,
		Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "none"},
	}
	patch := []byte("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-initial\n+applied\n")
	request := app.PatchTargetRequest{TaskID: domain.NewTaskID(), Raw: patch, PatchDigest: sha256.Sum256(patch), AffectedPaths: []string{"README.md"}}
	return resourcePatchTargetFixture{root: root, resource: resource, request: request, target: newResourcePatchTarget()}
}

func writeResourcePatchTargetFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
