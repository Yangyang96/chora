package speccoding

const PiRuntimeConfigSchemaVersionV6 = "chora.pi-runtime-config.v6"

// PiRuntimeConfigV6 adds one bounded, user-visible projection: completed
// assistant text from Pi message_end events. Thinking, partial deltas, raw
// prompts, tool arguments, and credential values remain outside persistence.
type PiRuntimeConfigV6 = PiRuntimeConfigV4

func DecodePiRuntimeConfigV6(data []byte) (PiRuntimeConfigV6, error) {
	var config PiRuntimeConfigV6
	if err := decodeCandidatePolicy(data, &config); err != nil {
		return PiRuntimeConfigV6{}, err
	}
	if err := validatePiRuntimeConfigV6(config); err != nil {
		return PiRuntimeConfigV6{}, err
	}
	return config, nil
}

func validatePiRuntimeConfigV6(config PiRuntimeConfigV6) error {
	wantEvents := []string{"agent_start", "agent_end", "assistant_message", "tool_start", "tool_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage"}
	wantFields := []string{"event_type", "timestamp", "text", "truncated", "tool_name", "workspace_relative_path", "redacted_command_summary", "exit_code", "error_class", "model_id", "model_digest", "usage"}
	if config.SchemaVersion != PiRuntimeConfigSchemaVersionV6 ||
		!slicesEqual(config.EventProjection.AllowedEvents, wantEvents) ||
		!slicesEqual(config.EventProjection.AllowedFields, wantFields) ||
		config.EventProjection.PersistThinking || config.EventProjection.PersistRawPrompt || config.EventProjection.PersistCredentialValues ||
		config.EventProjection.CanonicalEventOwner != "chora" ||
		config.ResultAuthority.PiFinalTextRole != "completed_assistant_text_only" {
		return invalidCandidatePolicy("v6 assistant text projection")
	}

	legacy := PiRuntimeConfigV5(config)
	legacy.SchemaVersion = PiRuntimeConfigSchemaVersionV5
	legacy.EventProjection.AllowedEvents = []string{"agent_start", "agent_end", "tool_start", "tool_end", "file_path", "command_summary", "exit_code", "error", "model_identity", "usage"}
	legacy.EventProjection.AllowedFields = []string{"event_type", "timestamp", "tool_name", "workspace_relative_path", "redacted_command_summary", "exit_code", "error_class", "model_id", "model_digest", "usage"}
	legacy.ResultAuthority.PiFinalTextRole = "agent_summary_only"
	return validatePiRuntimeConfigV5(legacy)
}
