package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestRunResourceCheckFingerprintReadsStrictStdinAndWritesExactResponse(t *testing.T) {
	fixtureRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(fixtureRoot, "source")
	taskRoot := filepath.Join(fixtureRoot, "task")
	if err := os.Mkdir(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(taskRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	repoID := domain.NewRepositoryID().String()
	repositoryRoot := filepath.Join(taskRoot, repoID)
	if err := os.WriteFile(filepath.Join(sourceRoot, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runResourceFingerprintGit(t, sourceRoot, "init")
	runResourceFingerprintGit(t, sourceRoot, "add", "README.md")
	runResourceFingerprintGit(t, sourceRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "initial")
	head := strings.TrimSpace(runResourceFingerprintGit(t, sourceRoot, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runResourceFingerprintGit(t, sourceRoot, "rev-parse", "HEAD^{tree}"))
	runResourceFingerprintGit(t, sourceRoot, "worktree", "add", "--detach", repositoryRoot, head)
	request := map[string]any{
		"config": map[string]any{
			"schema": "chora.resource-observer-config.v1", "attemptId": domain.NewAttemptID().String(), "taskRoot": taskRoot,
			"resources": []map[string]any{{
				"repo_id": repoID, "name": "repository", "locator": repoID, "target_identity_digest": strings.Repeat("a", 64),
				"role": "write", "base_commit": head, "base_tree": tree, "base_ref": "HEAD",
				"scope":  map[string]any{"mode": "repository", "writableFiles": []string{}, "writableDirectories": []string{}, "protectedDirectories": []string{}, "migrationChoice": "not_needed"},
				"checks": map[string]any{"mode": "named", "commands": []map[string]any{{"id": "test", "name": "Tests", "version": 0, "command": "go test ./...", "argv": []string{"go", "test", "./..."}, "workingDirectory": ".", "source": "user"}}, "preparation": []any{}, "selectionSource": "user"},
			}},
		},
		"command": "cd " + repoID + " && go test ./...",
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runResourceCheckFingerprint(nil, bytes.NewReader(raw), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperRaw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	wantHelper := sha256.Sum256(helperRaw)
	if len(response) != 7 || response["schema"] != "chora.resource-fingerprint.v1" || response["algorithm"] != "sha256-resource-patch-v1" || response["repoId"] != repoID || response["workingDirectory"] != "." || len(response["commandDigest"].(string)) != 64 || response["fingerprint"] != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || response["helperSha256"] != hex.EncodeToString(wantHelper[:]) {
		t.Fatalf("response=%s", stdout.Bytes())
	}
}

func TestRunResourceCheckFingerprintRejectsArgumentsAndInvalidStdin(t *testing.T) {
	tests := []struct {
		name string
		args []string
		raw  []byte
		code int
	}{
		{name: "argument", args: []string{"unexpected"}, raw: []byte(`{}`), code: 2},
		{name: "unknown field", raw: []byte(`{"config":{},"command":"x","extra":true}`), code: 1},
		{name: "trailing JSON", raw: []byte(`{} {}`), code: 1},
		{name: "oversize", raw: bytes.Repeat([]byte("x"), (1<<20)+1), code: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := runResourceCheckFingerprint(test.args, bytes.NewReader(test.raw), &stdout, &stderr); got != test.code {
				t.Fatalf("exit=%d want=%d", got, test.code)
			}
			if stdout.Len() != 0 || strings.TrimSpace(stderr.String()) == "" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func runResourceFingerprintGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}
