package localweb

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func automaticChecksGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func automaticChecksRepository(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	automaticChecksGit(t, root, "init", "-q")
	automaticChecksGit(t, root, "config", "user.name", "Chora Test")
	automaticChecksGit(t, root, "config", "user.email", "chora@example.invalid")
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	automaticChecksGit(t, root, "add", ".")
	automaticChecksGit(t, root, "commit", "-qm", "fixture")
	return root, automaticChecksGit(t, root, "rev-parse", "HEAD")
}

func TestDiscoverAutomaticChecksReadsCommittedConfigurationOnly(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{
		"package.json": `{"scripts":{"test":"node committed.js"}}`,
	})
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"lint":"dirty-only"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checks, err := discoverAutomaticChecks(context.Background(), root, revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Name != "Node test" || !reflect.DeepEqual(checks[0].Argv, []string{"npm", "run", "test"}) || checks[0].WorkingDirectory != "." || checks[0].Source != "automatic" || checks[0].Version != 1 {
		t.Fatalf("checks=%+v", checks)
	}
	if checks[0].Command != "npm run test" || !strings.HasPrefix(checks[0].ID, "auto-") {
		t.Fatalf("check identity=%+v", checks[0])
	}
	again, err := discoverAutomaticChecks(context.Background(), root, revision, []string{"."})
	if err != nil || !reflect.DeepEqual(again, checks) {
		t.Fatalf("deterministic discovery=%+v err=%v", again, err)
	}
}

func TestDiscoverAutomaticChecksSupportsKnownCommittedEntrypoints(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{
		"package.json":   `{"scripts":{"typecheck":"tsc","lint":"eslint .","build":"vite build"}}`,
		"Makefile":       "test:\n\t@true\nignored:\n\t@true\n",
		"go.mod":         "module example.test/checks\n\ngo 1.25\n",
		"pyproject.toml": "[tool.pytest.ini_options]\naddopts = '-q'\n",
		"pom.xml":        "<project></project>\n",
	})
	checks, err := discoverAutomaticChecks(context.Background(), root, revision, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"npm", "run", "typecheck"}, {"npm", "run", "lint"}, {"npm", "run", "build"},
		{"make", "test"}, {"go", "test", "./..."}, {"python", "-m", "pytest"}, {"mvn", "test"},
	}
	if len(checks) != len(want) {
		t.Fatalf("checks=%+v", checks)
	}
	for index := range want {
		if !reflect.DeepEqual(checks[index].Argv, want[index]) {
			t.Fatalf("check[%d]=%v want %v", index, checks[index].Argv, want[index])
		}
	}
}

func TestDiscoverAutomaticChecksUsesExplicitDeepModulesWithoutTreeListing(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{
		"README.md":                       "root has no checks\n",
		"services/deep/api/package.json":  `{"scripts":{"test":"vitest"}}`,
		"services/deep/api/ignored.bin":   "not inspected",
		"services/another/api/pytest.ini": "[pytest]\n",
	})
	checks, err := discoverAutomaticChecks(context.Background(), root, revision, []string{"services/deep/api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].WorkingDirectory != "services/deep/api" || checks[0].Name != "Node test" {
		t.Fatalf("checks=%+v", checks)
	}
}

func TestDiscoverAutomaticChecksNoApplicableIsEmpty(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{"README.md": "nothing to discover\n"})
	checks, err := discoverAutomaticChecks(context.Background(), root, revision, nil)
	if err != nil || len(checks) != 0 {
		t.Fatalf("checks=%+v err=%v", checks, err)
	}
}

func TestDiscoverAutomaticChecksBoundsCommittedConfig(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{
		"package.json": `{"padding":"` + strings.Repeat("x", domain.RepositoryMetadataBytes) + `"}`,
	})
	checks, err := discoverAutomaticChecks(context.Background(), root, revision, nil)
	if !errors.Is(err, errGitMetadataLimit) || checks != nil {
		t.Fatalf("checks=%+v err=%v, want metadata limit", checks, err)
	}
}

func TestDiscoverAutomaticChecksHonorsCancellation(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{"go.mod": "module example.test/cancel\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks, err := discoverAutomaticChecks(ctx, root, revision, nil)
	if !errors.Is(err, context.Canceled) || checks != nil {
		t.Fatalf("checks=%+v err=%v, want context.Canceled", checks, err)
	}
}

func TestDiscoverAutomaticChecksRejectsUnsafeOrUnboundedDirectories(t *testing.T) {
	root, revision := automaticChecksRepository(t, map[string]string{"go.mod": "module example.test/bounds\n"})
	for _, directories := range [][]string{{"../escape"}, {".git"}, make([]string, maxAutomaticCheckDirectories+1)} {
		if _, err := discoverAutomaticChecks(context.Background(), root, revision, directories); !errors.Is(err, errAutomaticCheckDiscovery) {
			t.Fatalf("directories=%v error=%v", directories, err)
		}
	}
}
