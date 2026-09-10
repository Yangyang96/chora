package buildinfo

import "testing"

func TestCurrentRequiresVersion(t *testing.T) {
	got := Current()
	if got.Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestCurrentReturnsConfiguredVersion(t *testing.T) {
	originalVersion := Version
	Version = "test-version"
	t.Cleanup(func() {
		Version = originalVersion
	})

	got := Current()
	if got.Version != "test-version" {
		t.Fatalf("Version = %q, want %q", got.Version, "test-version")
	}
}
