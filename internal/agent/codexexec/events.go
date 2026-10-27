package codexexec

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/execution"
)

const maxFrame = 256 * 1024
const maxMessage = 64 * 1024

// DecodeEvent is stateless: offsets/replay are owned by Chora, and separate
// sessions can share the provider. Ordering is checked over persisted events at
// completion, never in mutable adapter state. Native diagnostics/tool output and
// reasoning are discarded; only bounded allowlisted fields reach persistence.
func (a *Adapter) DecodeEvent(c execution.EventChunk) (execution.DecodedChunk, error) {
	if c.Stream == execution.StreamStderr {
		return execution.DecodedChunk{ConsumedBytes: len(c.Data)}, nil
	}
	if c.Stream != execution.StreamStdout {
		return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
	}
	end := bytes.LastIndexByte(c.Data, '\n') + 1
	if (c.EOF && end != len(c.Data)) || len(c.Data)-end > maxFrame {
		return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
	}
	result := execution.DecodedChunk{ConsumedBytes: end}
	for _, line := range bytes.Split(c.Data[:end], []byte{'\n'})[:bytes.Count(c.Data[:end], []byte{'\n'})] {
		if len(line) == 0 || len(line) > maxFrame || !utf8.Valid(line) {
			return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
		}
		e, keep, err := project(line)
		if err != nil {
			return execution.DecodedChunk{}, err
		}
		if keep {
			result.Events = append(result.Events, e)
		}
	}
	return result, nil
}
func event(kind string, payload map[string]any) execution.NormalizedEvent {
	payload["event_type"] = kind
	b, _ := json.Marshal(payload)
	return execution.NormalizedEvent{Type: kind, NormalizedJSON: b}
}
func invalid() (execution.NormalizedEvent, bool, error) {
	return execution.NormalizedEvent{}, false, execution.ErrEventStreamInvalid
}
func identifier(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") == ""
}
func failureClass(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "model_not_found") || strings.Contains(lower, "model is not supported") {
		return "model_unavailable"
	}
	if strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication") {
		return "authentication_rejected"
	}
	return "provider_failure"
}
func project(line []byte) (execution.NormalizedEvent, bool, error) {
	var f struct {
		Type    string `json:"type"`
		Thread  string `json:"thread_id"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &f) != nil || f.Type == "" {
		return invalid()
	}
	switch f.Type {
	case "thread.started":
		if !identifier(f.Thread) {
			return invalid()
		}
		e := event("runtime_session", map[string]any{"runtime_id": AdapterID})
		e.ExternalSession = f.Thread
		return e, true, nil
	case "turn.started":
		return event("turn_start", map[string]any{}), true, nil
	case "turn.completed":
		var terminal map[string]json.RawMessage
		if json.Unmarshal(line, &terminal) != nil || len(terminal) != 2 || terminal["usage"] == nil {
			return invalid()
		}
		var usage map[string]int64
		if json.Unmarshal(terminal["usage"], &usage) != nil || len(usage) != 3 {
			return invalid()
		}
		for _, key := range []string{"input_tokens", "cached_input_tokens", "output_tokens"} {
			if value, ok := usage[key]; !ok || value < 0 {
				return invalid()
			}
		}
		return event("agent_settled", map[string]any{"outcome": "completed"}), true, nil
	case "turn.failed":
		if f.Error.Message == "" {
			return invalid()
		}
		return event("runtime_failure", map[string]any{"failure": failureClass(f.Error.Message)}), true, nil
	case "error":
		return event("runtime_failure", map[string]any{"failure": failureClass(f.Message)}), true, nil
	case "item.started", "item.updated", "item.completed":
		if !identifier(f.Item.ID) || f.Item.Type == "" {
			return invalid()
		}
		if f.Item.Type == "agent_message" && f.Type == "item.completed" {
			if strings.TrimSpace(f.Item.Text) == "" || len(f.Item.Text) > maxMessage {
				return invalid()
			}
			return event("assistant_message", map[string]any{"text": f.Item.Text, "terminal_complete": true, "item_id": f.Item.ID}), true, nil
		}
		if f.Item.Type == "command_execution" || f.Item.Type == "file_change" || f.Item.Type == "mcp_tool_call" {
			if f.Item.Type == "mcp_tool_call" {
				return invalid()
			} // Not in the launch policy.
			kind := "tool_start"
			if f.Type == "item.completed" {
				kind = "tool_result"
			}
			if f.Type == "item.updated" {
				return execution.NormalizedEvent{}, false, nil
			}
			return event(kind, map[string]any{"call_id": f.Item.ID, "tool": f.Item.Type}), true, nil
		}
		// Warnings (item.error), reasoning and unknown informational items are not
		// terminal protocol outcomes and never become persisted diagnostics.
		return execution.NormalizedEvent{}, false, nil
	default:
		if strings.HasPrefix(f.Type, "turn.") || strings.HasPrefix(f.Type, "thread.") {
			return invalid()
		}
		return execution.NormalizedEvent{}, false, nil
	}
}
func failed(reason string) execution.TerminalResult {
	return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: reason}
}
func (a *Adapter) DecodeTerminal(files execution.TerminalFiles) (execution.TerminalResult, error) {
	if files.TerminationCause != execution.TerminationNone && files.TerminationCause != "" {
		return failed(string(files.TerminationCause)), nil
	}
	if files.ExitCode != 0 {
		return failed("process_failure"), nil
	}
	// Exit alone is insufficient. Complete adds the persisted protocol proof.
	return failed("protocol_incomplete"), nil
}
func (a *Adapter) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	if r.Files.TerminationCause != execution.TerminationNone && r.Files.TerminationCause != "" && r.Files.TerminationCause != execution.TerminationExitNonzero {
		return failed(string(r.Files.TerminationCause)), nil
	}
	started, thread, turn, settled := false, false, false, false
	final := ""
	seen := map[string]bool{}
	failure := ""
	for _, e := range r.Events {
		if e.Type() == "attempt.started" {
			started = true
			thread = false
			turn = false
			settled = false
			final = ""
			failure = ""
			seen = map[string]bool{}
			continue
		}
		if !started || e.Source() != "adapter" {
			continue
		}
		var p struct {
			Text     string `json:"text"`
			ID       string `json:"item_id"`
			Failure  string `json:"failure"`
			Outcome  string `json:"outcome"`
			Complete bool   `json:"terminal_complete"`
		}
		if json.Unmarshal(e.NormalizedJSON(), &p) != nil {
			return failed("protocol_invalid"), nil
		}
		if e.Type() == "runtime_failure" {
			failure = p.Failure
			continue
		}
		switch e.Type() {
		case "runtime_session":
			if thread || turn {
				return failed("protocol_invalid"), nil
			}
			thread = true
		case "turn_start":
			if !thread || turn || settled {
				return failed("protocol_invalid"), nil
			}
			turn = true
		case "assistant_message":
			if !turn || settled || !p.Complete || p.Text == "" || len(p.Text) > maxMessage || seen[p.ID] {
				return failed("protocol_invalid"), nil
			}
			seen[p.ID] = true
			final = p.Text
		case "agent_settled":
			if !turn || settled || p.Outcome != "completed" {
				return failed("protocol_invalid"), nil
			}
			settled = true
		case "tool_start", "tool_result":
			if !turn || settled {
				return failed("protocol_invalid"), nil
			}
		}
	}
	if failure != "" {
		return failed(failure), nil
	}
	if r.Files.ExitCode != 0 {
		return failed("process_failure"), nil
	}
	if !started || !settled || final == "" {
		return failed("protocol_incomplete"), nil
	}
	return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: final}, nil
}
