package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/sourcebundle"
)

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(nil, &stdout, &stderr); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if stdout.Len() != 0 || stderr.String() == "" {
		t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
	}
}

func TestRunCheckPolicyRejectsGroupWorldWritableSourceFile(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	createPolicyFixture(t, source)
	if err := os.Chmod(filepath.Join(source, "package.json"), 0o666); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if got := run([]string{"check-policy", "--source", source}, &stdout, &stderr); got != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %q", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "source file is group/world writable: package.json") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func createPolicyFixture(t *testing.T, source string) {
	t.Helper()
	files := map[string]string{
		".npmrc":             "engine-strict=true\nregistry=https://registry.npmjs.org/\n",
		"go.mod":             "module github.com/Yangyang96/chora\n",
		"package.json":       "{\"name\":\"chora\",\"private\":true}\n",
		"vendor/modules.txt": "fixture vendor metadata\n",
	}
	for path, body := range files {
		writePolicyFixture(t, filepath.Join(source, filepath.FromSlash(path)), []byte(body))
	}

	paths := []string{
		".npmrc",
		sourcebundle.SourcePathPolicyPath,
		"go.mod",
		"package.json",
		"vendor/modules.txt",
	}
	installOnlyPaths := []string{"package.json", "vendor/modules.txt"}
	modelReadablePaths := []string{".npmrc", sourcebundle.SourcePathPolicyPath, "go.mod"}
	policy := map[string]any{
		"schema_version":              sourcebundle.SourcePathPolicySchema,
		"roots":                       []string{".npmrc", "distribution", "go.mod", "package.json", "vendor"},
		"excluded_directory_names":    []string{".vite"},
		"excluded_file_suffixes":      []string{".tsbuildinfo"},
		"declared_files":              len(paths),
		"declared_paths_sha256":       policyPathsDigest(paths),
		"install_only_roots":          []string{"vendor"},
		"install_only_paths":          []string{"package.json"},
		"install_only_files":          len(installOnlyPaths),
		"install_only_paths_sha256":   policyPathsDigest(installOnlyPaths),
		"model_readable_files":        len(modelReadablePaths),
		"model_readable_paths_sha256": policyPathsDigest(modelReadablePaths),
	}
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePolicyFixture(t, filepath.Join(source, filepath.FromSlash(sourcebundle.SourcePathPolicyPath)), append(encoded, '\n'))
}

func writePolicyFixture(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func policyPathsDigest(paths []string) string {
	hash := sha256.New()
	for _, path := range paths {
		fmt.Fprintf(hash, "%s\n", path)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
