package isolatedworkspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestPrepareAndCollectTwoRepositories(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	resources := []domain.TaskRepositoryResource{
		makeRepository(t, source, "one", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"keep.txt": "alpha\n", "delete.txt": "gone\n"}),
		makeRepository(t, source, "two", "write", domain.TaskRepositoryScope{Mode: "restricted", WritableDirectories: []string{"src"}}, map[string]string{"src/code.txt": "old\n"}),
	}
	if err := Prepare(context.Background(), source, dest, state, resources); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(dest, resources[0].WorkspaceDirectory(), "keep.txt"), []byte("alpha\n"))
	if info, err := os.Lstat(filepath.Join(dest, resources[0].WorkspaceDirectory(), ".git")); err != nil || !info.IsDir() {
		t.Fatalf("standalone .git: %v", err)
	}
	gitRoot := filepath.Join(dest, resources[0].WorkspaceDirectory(), ".git")
	if _, err := os.Lstat(filepath.Join(gitRoot, "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("FETCH_HEAD retained: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(gitRoot, "logs")); !os.IsNotExist(err) {
		t.Fatalf("reflogs retained: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, resources[0].WorkspaceDirectory(), "keep.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dest, resources[0].WorkspaceDirectory(), "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, resources[1].WorkspaceDirectory(), "src", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), source, dest, state, resources); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "keep.txt"), []byte("changed\n"))
	if _, err := os.Stat(filepath.Join(source, resources[0].WorkspaceDirectory(), "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file remains: %v", err)
	}
	assertBytes(t, filepath.Join(source, resources[1].WorkspaceDirectory(), "src", "new.txt"), []byte("new\n"))
}

func TestCollectRejectsUnsafeResultsAndSourceDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, source, dest string, r domain.TaskRepositoryResource)
	}{
		{"reference change", func(t *testing.T, _, d string, r domain.TaskRepositoryResource) {
			mustWrite(t, filepath.Join(d, r.WorkspaceDirectory(), "a.txt"), "bad\n")
		}},
		{"git tamper", func(t *testing.T, _, d string, r domain.TaskRepositoryResource) {
			mustWrite(t, filepath.Join(d, r.WorkspaceDirectory(), ".git", "config"), "bad\n")
		}},
		{"symlink", func(t *testing.T, _, d string, r domain.TaskRepositoryResource) {
			if err := os.Symlink("/tmp", filepath.Join(d, r.WorkspaceDirectory(), "escape")); err != nil {
				t.Fatal(err)
			}
		}},
		{"source drift", func(t *testing.T, s, _ string, r domain.TaskRepositoryResource) {
			mustWrite(t, filepath.Join(s, r.WorkspaceDirectory(), "a.txt"), "external\n")
		}},
		{"out of scope", func(t *testing.T, _, d string, r domain.TaskRepositoryResource) {
			mustWrite(t, filepath.Join(d, r.WorkspaceDirectory(), "a.txt"), "bad\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
			role := "write"
			scope := domain.TaskRepositoryScope{Mode: "repository"}
			if tc.name == "reference change" {
				role = "reference"
			}
			if tc.name == "out of scope" {
				scope = domain.TaskRepositoryScope{Mode: "restricted", WritableDirectories: []string{"src"}}
			}
			r := makeRepository(t, source, "repo", role, scope, map[string]string{"a.txt": "base\n"})
			if err := Prepare(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, source, dest, r)
			if err := Collect(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err == nil {
				t.Fatal("Collect accepted unsafe result")
			}
		})
	}
}

func TestCollectRecoversInterruptedImportBeforeApplying(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	r := makeRepository(t, source, "repo", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"a.txt": "base\n"})
	if err := Prepare(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(source, r.WorkspaceDirectory(), "a.txt"), "partial import\n")
	mustWrite(t, filepath.Join(state, "import-journal.json"), `{"state":"applying"}`)
	mustWrite(t, filepath.Join(dest, r.WorkspaceDirectory(), "a.txt"), "result\n")
	if err := Collect(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(source, r.WorkspaceDirectory(), "a.txt"), []byte("result\n"))
}

func TestCollectIgnoresReadonlySealAndPreservesReferenceModes(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	r := makeRepository(t, source, "reference", "reference", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"dir/a.txt": "base\n"})
	if err := Prepare(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dest, r.WorkspaceDirectory())
	if err := os.Chmod(filepath.Join(root, "dir", "a.txt"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "dir"), 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "dir"), 0755)
	if err := Collect(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(source, r.WorkspaceDirectory(), "dir", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("source mode=%o", info.Mode().Perm())
	}
}

func TestCollectTreatsExecutableBitAsGitChange(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	r := makeRepository(t, source, "write", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"script.sh": "#!/bin/sh\n"})
	if err := Prepare(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dest, r.WorkspaceDirectory(), "script.sh"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), source, dest, state, []domain.TaskRepositoryResource{r}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(source, r.WorkspaceDirectory(), "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("source mode=%o", info.Mode().Perm())
	}
}

func makeRepository(t *testing.T, source, name, role string, scope domain.TaskRepositoryScope, files map[string]string) domain.TaskRepositoryResource {
	t.Helper()
	id := domain.NewRepositoryID().String()
	locator := domain.ShortWorkspaceName(id)
	root := filepath.Join(source, locator)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		mustWrite(t, filepath.Join(root, path), content)
	}
	testRunGit(t, root, "init", "-q")
	testRunGit(t, root, "add", ".")
	testRunGit(t, root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	commit := strings.TrimSpace(testRunGit(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(testRunGit(t, root, "rev-parse", "HEAD^{tree}"))
	common := strings.TrimSpace(testRunGit(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	return domain.TaskRepositoryResource{RepoID: id, Name: name, Checkout: root, CommonGitDir: common, PhysicalIdentity: strings.Repeat("a", 64), Role: role, BaseCommit: commit, BaseTree: tree, BaseRef: "refs/heads/main", TaskBranch: "chora/test", AssociationVersion: 1, Scope: scope, WorkspaceName: locator}
}

func testRunGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return string(b)
}
func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}
func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, e := os.ReadFile(path)
	if e != nil || string(got) != string(want) {
		t.Fatalf("%s = %q, %v", path, got, e)
	}
}
