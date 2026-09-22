//go:build chora_e2e

package localweb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicE2EBaselineIsSelfContainedAndRejectsDrift(t *testing.T) {
	server := &Server{runtimeRoot: t.TempDir(), verifier: &productVerifier{}}
	if err := prepareE2EBaseline(server); err != nil {
		t.Fatal(err)
	}
	if err := server.verifier.VerifyBaselineIdentity(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(server.verifier.baselineRoot, "internal", "domain", "task.go")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := server.verifier.VerifyBaselineIdentity(); err == nil {
		t.Fatal("accepted modified fixture baseline")
	}
	if err := prepareE2EBaseline(server); err == nil {
		t.Fatal("silently replaced modified baseline")
	}
}
