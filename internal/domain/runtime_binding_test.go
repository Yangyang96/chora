package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeBindingV1RoundTripAndLegacyRepresentation(t *testing.T) {
	r := RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: "synthetic", ProtocolVersion: "v1", SourceIdentity: strings.Repeat("a", 64), ConfigurationFingerprint: strings.Repeat("b", 64), ExecutionProvider: TrustedHostExecutionProvider, SecurityFingerprint: strings.Repeat("c", 64), CapabilityDigest: strings.Repeat("d", 64), ModelProvenance: "native_default"}
	b, err := NewRuntimeProfileBinding(r)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreAgentExecutionProfileBinding(b.Record())
	if err != nil || restored != b || !validAgentExecutionProfileBinding("synthetic", b) || validAgentExecutionProfileBinding("different", b) {
		t.Fatalf("binding round trip: %v", err)
	}
	for _, id := range []string{"pi", "codex", "fake", ""} {
		invalid := r
		invalid.RuntimeID = id
		if _, err := NewRuntimeProfileBinding(invalid); err == nil {
			t.Fatalf("accepted reserved ID %q", id)
		}
	}
	drift := b.Record()
	drift.RuntimeSource = "other"
	if _, err := RestoreAgentExecutionProfileBinding(drift); err == nil {
		t.Fatal("source drift accepted")
	}
	legacy, _ := NewAgentExecutionProfileBinding(AgentExecutionProfileTrustedLocal)
	raw, _ := json.Marshal(legacy.Record())
	if strings.Contains(string(raw), "RuntimeBinding") || !legacy.Legacy() || legacy.RuntimeID() != "pi" {
		t.Fatal("legacy representation changed")
	}
	if _, err := ParseRuntimeBinding(b.RuntimeBindingJSON() + ` {}`); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
