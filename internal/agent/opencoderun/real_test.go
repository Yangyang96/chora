package opencoderun

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

// TestRealOpenCodeQualificationRefusal performs metadata probes only. This is
// deliberately usable without model-call budget or credential material.
func TestRealOpenCodeQualificationRefusal(t *testing.T) {
	executable, home, model := os.Getenv("CHORA_OPENCODE_EXECUTABLE"), os.Getenv("CHORA_OPENCODE_HOME"), os.Getenv("CHORA_OPENCODE_MODEL")
	if executable == "" || home == "" || model == "" {
		t.Skip("explicit existing OpenCode executable/home/model required")
	}
	config := Config{Executable: executable, Home: home, Model: model,
		DataHome: os.Getenv("CHORA_OPENCODE_DATA_HOME"), ConfigHome: os.Getenv("CHORA_OPENCODE_CONFIG_HOME"),
		CacheHome: os.Getenv("CHORA_OPENCODE_CACHE_HOME"), StateHome: os.Getenv("CHORA_OPENCODE_STATE_HOME")}
	r := Inspect(context.Background(), config)
	if r.Ready || r.Qualification == nil || r.Qualification.State != execution.QualificationRejected {
		t.Fatalf("unexpected readiness %+v", r)
	}
	_, err := New(context.Background(), config)
	if !errors.Is(err, ErrNativeConfig) || !errors.Is(err, ErrNativeRetry) {
		t.Fatal("unsafe native launch accepted", err)
	}
	t.Logf("native version %s, source %x, state %s", r.Qualification.Identity.Version, r.Qualification.Identity.SourceIdentity, r.Qualification.State)
	for _, e := range r.Qualification.Evidence {
		t.Logf("%s %s %s %s", e.Capability, e.State, e.Method, e.Reference)
	}
}
