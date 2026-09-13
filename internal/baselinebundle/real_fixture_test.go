package baselinebundle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRealFrozenBaselineV4Fixture(t *testing.T) {
	if os.Getenv("CHORA_BASELINE_BUNDLE_REAL") != "1" {
		t.Skip("set CHORA_BASELINE_BUNDLE_REAL=1 for the 139 MiB reconstruction gate")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixtureRoot := filepath.Join(repositoryRoot, "contracts", "g2-m4", "source-baseline-v4")
	manifest, err := os.ReadFile(filepath.Join(fixtureRoot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "baseline-v4")
	result, err := Reconstruct(context.Background(), Request{
		InstalledRoot: repositoryRoot,
		DeltaRoot:     filepath.Join(fixtureRoot, "delta"),
		TargetRoot:    target,
		Manifest:      manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AggregateSHA256 != "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd" || result.EntryCount != 2193 || result.DeltaEntries != 82 {
		t.Fatalf("real fixture result = %#v", result)
	}
}
