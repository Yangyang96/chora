//go:build isolated_acceptance

package isolatedenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareAndLoadRealPublicEnvironment(t *testing.T) {
	data := os.Getenv("CHORA_REAL_ISOLATED_DATA_ROOT")
	if data == "" {
		t.Fatal("CHORA_REAL_ISOLATED_DATA_ROOT is required")
	}
	source, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	record, err := Prepare(context.Background(), source, data)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Source.Configured() || record.Source.ImageID() == "" || !record.Qualification.ValidFor(record.EngineIdentity, record.Capability) {
		t.Fatal("prepared record is incomplete")
	}
	loaded, err := Load(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Source.SourceIdentity() != record.Source.SourceIdentity() {
		t.Fatal("loaded source identity drifted")
	}
}
