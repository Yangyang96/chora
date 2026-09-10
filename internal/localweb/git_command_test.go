package localweb

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestProductGitCommandsIgnoreAmbientGitAuthorityAndPATH(t *testing.T) {
	patchRoot := newPatchTargetRepository(t)
	anchor, _, revision := newTaskWorktreeTestRepository(t)
	poison := t.TempDir()
	if err := os.WriteFile(filepath.Join(poison, "git"), []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(poison, "global.gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[core]\n\thooksPath = /forbidden/hooks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", poison)
	t.Setenv("HOME", poison)
	t.Setenv("XDG_CONFIG_HOME", poison)
	t.Setenv("GIT_DIR", filepath.Join(poison, "foreign.git"))
	t.Setenv("GIT_WORK_TREE", poison)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/forbidden/injected-hooks")
	t.Setenv("GIT_EXEC_PATH", poison)
	t.Setenv("GIT_ASKPASS", filepath.Join(poison, "git"))
	t.Setenv("SSH_ASKPASS", filepath.Join(poison, "git"))

	target, err := newGitPatchTarget(patchRoot, nil)
	if err != nil {
		t.Fatalf("patch target inherited ambient Git authority: %v", err)
	}
	patch := []byte("diff --git a/internal/value.go b/internal/value.go\n--- a/internal/value.go\n+++ b/internal/value.go\n@@ -1,3 +1,3 @@\n package internal\n \n-func Value() int { return 1 }\n+func Value() int { return 2 }\n")
	request := app.PatchTargetRequest{Raw: patch, PatchDigest: sha256Bytes(patch), AffectedPaths: []string{"internal/value.go"}}
	inspection, err := target.Inspect(context.Background(), request)
	if err != nil || !inspection.Applicable {
		t.Fatalf("patch inspection inherited ambient Git authority: inspection=%#v err=%v", inspection, err)
	}

	if _, err := newTaskWorktreeManager(anchor, speccoding.RepositoryIdentity{
		Name: "chora", SourceRevision: revision,
	}); err != nil {
		t.Fatalf("worktree manager inherited ambient Git authority: %v", err)
	}
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}
