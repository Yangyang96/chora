package speccoding

import (
	"encoding/json"
	"testing"
)

func TestPiRuntimeConfigV6AddsOnlyBoundedAssistantTextProjection(t *testing.T) {
	config, err := DecodePiRuntimeConfigV6(readV4ContractFixture(t, "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	if config.ResultAuthority.PiFinalTextRole != "completed_assistant_text_only" || config.EventProjection.PersistThinking || config.EventProjection.PersistRawPrompt || config.EventProjection.PersistCredentialValues {
		t.Fatalf("unsafe v6 projection = %#v %#v", config.EventProjection, config.ResultAuthority)
	}

	unsafe := config
	unsafe.EventProjection.PersistThinking = true
	encoded, _ := json.Marshal(unsafe)
	if _, err := DecodePiRuntimeConfigV6(encoded); err == nil {
		t.Fatal("v6 accepted thinking persistence")
	}

	unsafe = config
	unsafe.EventProjection.AllowedEvents = append(unsafe.EventProjection.AllowedEvents, "message_update")
	encoded, _ = json.Marshal(unsafe)
	if _, err := DecodePiRuntimeConfigV6(encoded); err == nil {
		t.Fatal("v6 accepted partial assistant deltas")
	}
}
