package codexdiscovery

import (
	"context"
	"strings"
	"testing"
)

func TestReadOnlyProbeRejectsExecutionAndBoundsDiagnostics(t *testing.T) {
	for _, args := range [][]string{{"exec"}, {"login"}, {"logout"}, {"login", "--device-auth"}} {
		if _, err := Probe(context.Background(), "/nonexistent", "/nonexistent", "/nonexistent", args...); err != ErrProbe {
			t.Fatal("mutating probe accepted")
		}
	}
	b := &boundedOutput{}
	_, _ = b.Write([]byte(strings.Repeat("x", 4097)))
	if !b.exceeded || len(b.data) > 4096 {
		t.Fatal("unbounded output")
	}
}
