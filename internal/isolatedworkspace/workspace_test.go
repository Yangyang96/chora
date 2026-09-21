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

func TestDocumentWorkspaceStaysEmpty(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	if err := PrepareDocument(source, dest, state); err != nil {
		t.Fatal(err)
	}
	if err := CollectDocument(source, dest, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "answer.md"), []byte("not authoritative"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CollectDocument(source, dest, state); err == nil {
		t.Fatal("accepted document workspace output")
	}
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

func TestRecoverInterruptedImportAcrossRepositories(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	resources := []domain.TaskRepositoryResource{
		makeRepository(t, source, "one", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"a.txt": "one base\n", "removed.txt": "base\n"}),
		makeRepository(t, source, "two", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"b.txt": "two base\n"}),
	}
	if err := Prepare(context.Background(), source, dest, state, resources); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dest, resources[0].WorkspaceDirectory(), "a.txt"), "one result\n")
	if err := os.Remove(filepath.Join(dest, resources[0].WorkspaceDirectory(), "removed.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dest, resources[0].WorkspaceDirectory(), "added.txt"), "added\n")
	mustWrite(t, filepath.Join(dest, resources[1].WorkspaceDirectory(), "b.txt"), "two result\n")
	writeInterruptedJournal(t, state, dest)

	// Simulate process death after the first repository was fully imported and
	// before the second repository was touched.
	after, err := scanTree(filepath.Join(dest, resources[0].WorkspaceDirectory()), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceWorktree(filepath.Join(source, resources[0].WorkspaceDirectory()), after); err != nil {
		t.Fatal(err)
	}

	if err := Recover(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "a.txt"), []byte("one base\n"))
	assertBytes(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "removed.txt"), []byte("base\n"))
	if _, err := os.Lstat(filepath.Join(source, resources[0].WorkspaceDirectory(), "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("added file survived rollback: %v", err)
	}
	assertBytes(t, filepath.Join(source, resources[1].WorkspaceDirectory(), "b.txt"), []byte("two base\n"))
	if _, err := os.Lstat(filepath.Join(state, "import-journal.json")); !os.IsNotExist(err) {
		t.Fatalf("resolved journal retained: %v", err)
	}
	if err := Recover(context.Background(), state); err != nil {
		t.Fatalf("repeated recovery: %v", err)
	}
}

func TestRecoverRejectsMissingOrTamperedManifestWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(t *testing.T, state string)
	}{
		{"missing", func(t *testing.T, state string) {
			if err := os.Remove(filepath.Join(state, "manifest.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered", func(t *testing.T, state string) {
			var m manifest
			if err := readJSONRegular(filepath.Join(state, "manifest.json"), &m); err != nil {
				t.Fatal(err)
			}
			entry := m.Repositories[0].Source["a.txt"]
			entry.Data = []byte("attacker\n")
			m.Repositories[0].Source["a.txt"] = entry
			if err := os.Remove(filepath.Join(state, "manifest.json")); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONExclusive(filepath.Join(state, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
		}},
		{"escape path", func(t *testing.T, state string) {
			var m manifest
			if err := readJSONRegular(filepath.Join(state, "manifest.json"), &m); err != nil {
				t.Fatal(err)
			}
			m.Repositories[0].Source["../outside"] = m.Repositories[0].Source["a.txt"]
			delete(m.Repositories[0].Source, "a.txt")
			if err := os.Remove(filepath.Join(state, "manifest.json")); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONExclusive(filepath.Join(state, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
			resources := []domain.TaskRepositoryResource{
				makeRepository(t, source, "one", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"a.txt": "one base\n"}),
				makeRepository(t, source, "two", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"b.txt": "two base\n"}),
			}
			if err := Prepare(context.Background(), source, dest, state, resources); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dest, resources[0].WorkspaceDirectory(), "a.txt"), "one result\n")
			writeInterruptedJournal(t, state, dest)
			mustWrite(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "a.txt"), "one result\n")
			tc.tamper(t, state)

			if err := Recover(context.Background(), state); err == nil {
				t.Fatal("Recover accepted unavailable or tampered manifest")
			}
			assertBytes(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "a.txt"), []byte("one result\n"))
			assertBytes(t, filepath.Join(source, resources[1].WorkspaceDirectory(), "b.txt"), []byte("two base\n"))
			if _, err := os.Lstat(filepath.Join(state, "import-journal.json")); err != nil {
				t.Fatalf("unresolved journal was not preserved: %v", err)
			}
		})
	}
}

func TestRecoverRejectsExternalDriftBeforeWritingAnyRepository(t *testing.T) {
	source, dest, state := t.TempDir(), t.TempDir(), t.TempDir()
	resources := []domain.TaskRepositoryResource{
		makeRepository(t, source, "one", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"a.txt": "one base\n"}),
		makeRepository(t, source, "two", "write", domain.TaskRepositoryScope{Mode: "repository"}, map[string]string{"b.txt": "two base\n"}),
	}
	if err := Prepare(context.Background(), source, dest, state, resources); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dest, resources[0].WorkspaceDirectory(), "a.txt"), "one result\n")
	writeInterruptedJournal(t, state, dest)
	mustWrite(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "a.txt"), "one result\n")
	mustWrite(t, filepath.Join(source, resources[1].WorkspaceDirectory(), "b.txt"), "external edit\n")

	if err := Recover(context.Background(), state); err == nil {
		t.Fatal("Recover overwrote external drift")
	}
	assertBytes(t, filepath.Join(source, resources[0].WorkspaceDirectory(), "a.txt"), []byte("one result\n"))
	assertBytes(t, filepath.Join(source, resources[1].WorkspaceDirectory(), "b.txt"), []byte("external edit\n"))
	if _, err := os.Lstat(filepath.Join(state, "import-journal.json")); err != nil {
		t.Fatalf("unresolved journal was not preserved: %v", err)
	}
}

func TestRecoverPreservesLegacyUnprovenJournal(t *testing.T) {
	state := t.TempDir()
	mustWrite(t, filepath.Join(state, "import-journal.json"), `{"state":"applying"}`)
	if err := Recover(context.Background(), state); err == nil {
		t.Fatal("Recover accepted legacy journal without rollback authority")
	}
	if _, err := os.Lstat(filepath.Join(state, "import-journal.json")); err != nil {
		t.Fatalf("legacy journal was not preserved: %v", err)
	}
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

func writeInterruptedJournal(t *testing.T, state, dest string) manifest {
	t.Helper()
	var m manifest
	if err := readJSONRegular(filepath.Join(state, "manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	j := importJournal{Version: journalVersion, ManifestDigest: manifestDigest(m)}
	for _, rs := range m.Repositories {
		after, err := scanTree(filepath.Join(dest, rs.Resource.WorkspaceDirectory()), false)
		if err != nil {
			t.Fatal(err)
		}
		j.Repositories = append(j.Repositories, journalRepo{RepoID: rs.Resource.RepoID, After: after})
	}
	if err := writeJSONExclusive(filepath.Join(state, "import-journal.json"), j); err != nil {
		t.Fatal(err)
	}
	return m
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
