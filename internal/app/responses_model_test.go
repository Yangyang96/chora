package app

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestAttemptResponsePreservesModelBindingAndReadsLegacyResponse(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	catalog, err := domain.NewModelCatalog("pi", "runtime", "1", []domain.ModelIdentity{{Provider: "deepseek", ModelID: "deepseek-v4-pro"}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewModelBinding(catalog, catalog.Models[0], now)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: domain.NewRunID(), Sequence: 1, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256([]byte("snapshot")), AdapterID: "pi", AgentExecutionProfileBinding: profile, ModelBinding: binding, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wireAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			delete(document, "ModelBinding")
		}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		var wire attemptWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		restored, err := restoreAttempt(wire)
		if err != nil || restored.ID() != attempt.ID() || restored.AgentExecutionProfileBinding() != profile {
			t.Fatalf("legacy=%v restore err=%v", legacy, err)
		}
		expected := binding
		if legacy {
			expected = domain.ModelBinding{}
		}
		if restored.ModelBinding() != expected {
			t.Fatalf("legacy=%v restored binding=%s", legacy, restored.ModelBinding().JSON())
		}
	}
}
