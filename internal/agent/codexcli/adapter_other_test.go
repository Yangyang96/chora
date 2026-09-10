//go:build !darwin

package codexcli

import (
	"context"
	"strings"
	"testing"
)

func TestAdapterFingerprintFailsClosedWithoutDarwinRuntimeGate(t *testing.T) {
	adapter, err := New(Config{
		RuntimeRoot:  t.TempDir(),
		SchemaPath:   "/missing/schema.json",
		GatePath:     "/missing/darwin-runtime-gate.json",
		ContractPath: "/missing/contract.md",
		ManagedPath:  "/usr/bin:/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Fingerprint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "read Codex gate evidence") {
		t.Fatalf("Fingerprint() error = %v, want unavailable Darwin gate rejection", err)
	}
}
