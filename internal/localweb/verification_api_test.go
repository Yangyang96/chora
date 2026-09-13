//go:build chora_e2e

package localweb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestE2EVerifierStartupCandidateRecoveryDoesNotUseAmbientDocker(t *testing.T) {
	t.Setenv(verifierPolicyModeEnvironment, "full")
	root := t.TempDir()
	commandBin := filepath.Join(root, "ambient-command-bin")
	if err := os.Mkdir(commandBin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "colima"} {
		if err := os.WriteFile(filepath.Join(commandBin, name), []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", commandBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &e2eVerifierSandboxFactory{}
	service, _, err := newProductVerifier(
		withVerifierSandboxLifecycle(context.Background(), lifecycle),
		repoRoot, filepath.Join(root, "runtime"), false, ProductOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.sandboxLifecycle != lifecycle {
		t.Fatalf("E2E lifecycle was replaced by %T", service.sandboxLifecycle)
	}
	listed := false
	count, err := recoverInstalledVerifierStartup(context.Background(), service, func(context.Context) ([]storecontract.VerificationStartupCandidate, error) {
		listed = true
		return make([]storecontract.VerificationStartupCandidate, 1), nil
	})
	if err != nil || count != 1 || !listed {
		t.Fatalf("startup recovery count=%d listed=%t err=%v", count, listed, err)
	}
}
