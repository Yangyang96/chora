package migrations

import (
	"fmt"
	"testing"
)

func TestCurrentVersionMatchesCompleteEmbeddedLedger(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != CurrentVersion {
		t.Fatalf("build supports %d; embedded %d", CurrentVersion, len(entries))
	}
	for n, e := range entries {
		if e.IsDir() || len(e.Name()) < 5 || e.Name()[:5] != fmt.Sprintf("%04d_", n+1) {
			t.Fatalf("noncontiguous migration %q", e.Name())
		}
	}
}
