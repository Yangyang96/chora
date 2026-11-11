package claudeprint

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/execution"
)

const maxFrame = 256 * 1024
const maxMessage = 64 * 1024

func identifier(s string) bool {
	return s != "" && len(s) <= 128 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") == ""
}
func event(kind string, payload map[string]any) execution.NormalizedEvent {
	payload["event_type"] = kind
	b, _ := json.Marshal(payload)
	return execution.NormalizedEvent{Type: kind, NormalizedJSON: b}
}
func failureClass(code string) string {
	switch code {
	case "authentication_failed":
		return "authentication_rejected"
	case "rate_limit":
		return "rate_limited"
	case "invalid_request":
		return "request_rejected"
	}
	return "provider_failure"
}
func allowedTool(name string) bool {
	switch name {
	case "Read", "Glob", "Grep", "Edit", "Write", "Bash":
		return true
	}
	return false
}

// DecodeEvent is stateless across chunks/sessions. Native diagnostics, thinking,
// tool arguments/output, paths and account metadata never enter persisted events.
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
		events, err := project(line)
		if err != nil {
			return execution.DecodedChunk{}, err
		}
		result.Events = append(result.Events, events...)
	}
	return result, nil
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

func project(line []byte) ([]execution.NormalizedEvent, error) {
	var f struct {
		Type              string            `json:"type"`
		Subtype           string            `json:"subtype"`
		Session           string            `json:"session_id"`
		Parent            *string           `json:"parent_tool_use_id"`
		Error             string            `json:"error"`
		APIError          bool              `json:"is_api_error_message"`
		IsError           *bool             `json:"is_error"`
		Result            string            `json:"result"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
		Tools             []string          `json:"tools"`
		MCP               []json.RawMessage `json:"mcp_servers"`
		Skills            []json.RawMessage `json:"skills"`
		Plugins           []json.RawMessage `json:"plugins"`
		Commands          []json.RawMessage `json:"slash_commands"`
		PermissionMode    string            `json:"permissionMode"`
		Version           string            `json:"claude_code_version"`
		Message           struct {
			ID      string         `json:"id"`
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	bad := func() ([]execution.NormalizedEvent, error) { return nil, execution.ErrEventStreamInvalid }
	if json.Unmarshal(line, &f) != nil || f.Type == "" {
		return bad()
	}
	if f.Parent != nil {
		return bad()
	} // Subagents are outside the declared tool set.
	if f.Type == "rate_limit_event" {
		return nil, nil
	}
	if !identifier(f.Session) {
		return bad()
	}
	payload := func() map[string]any { return map[string]any{"session_id": f.Session} }
	switch f.Type {
	case "system":
		if f.Subtype != "init" || f.Version != "2.1.251" || f.PermissionMode != "dontAsk" || len(f.MCP) != 0 || len(f.Skills) != 0 || len(f.Plugins) != 0 || len(f.Commands) != 0 {
			return bad()
		}
		// Check fields are actually present; missing arrays are not proof of no resources.
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(line, &raw)
		for _, key := range []string{"tools", "mcp_servers", "skills", "plugins", "slash_commands"} {
			if len(raw[key]) == 0 || raw[key][0] != '[' {
				return bad()
			}
		}
		seen := map[string]bool{}
		for _, tool := range f.Tools {
			if !allowedTool(tool) || seen[tool] {
				return bad()
			}
			seen[tool] = true
		}
		if len(seen) != 6 {
			return bad()
		}
		p := payload()
		p["runtime_id"] = AdapterID
		e := event("runtime_session", p)
		e.ExternalSession = f.Session
		return []execution.NormalizedEvent{e}, nil
	case "assistant":
		if f.APIError || f.Error != "" {
			p := payload()
			p["failure"] = failureClass(f.Error)
			return []execution.NormalizedEvent{event("runtime_failure", p)}, nil
		}
		if !identifier(f.Message.ID) || len(f.Message.Content) == 0 {
			return bad()
		}
		var events []execution.NormalizedEvent
		text := ""
		for _, block := range f.Message.Content {
			switch block.Type {
			case "text":
				text += block.Text
				if len(text) > maxMessage {
					return bad()
				}
			case "tool_use":
				if !identifier(block.ID) || !allowedTool(block.Name) {
					return bad()
				}
				p := payload()
				p["call_id"], p["tool"] = block.ID, block.Name
				events = append(events, event("tool_start", p))
			case "thinking", "redacted_thinking": // deliberately discarded
			default:
				return bad()
			}
		}
		if strings.TrimSpace(text) != "" {
			p := payload()
			p["text"], p["item_id"] = text, f.Message.ID
			events = append(events, event("assistant_message", p))
		}
		return events, nil
	case "user":
		var events []execution.NormalizedEvent
		if len(f.Message.Content) == 0 {
			return bad()
		}
		for _, block := range f.Message.Content {
			if block.Type != "tool_result" || !identifier(block.ToolUseID) {
				return bad()
			}
			p := payload()
			p["call_id"], p["is_error"] = block.ToolUseID, block.IsError
			events = append(events, event("tool_result", p))
		}
		return events, nil
	case "result":
		if f.IsError == nil {
			return bad()
		}
		p := payload()
		if *f.IsError || f.Subtype != "success" {
			p["failure"] = "provider_failure"
			return []execution.NormalizedEvent{event("runtime_failure", p)}, nil
		}
		if len(f.PermissionDenials) > 0 {
			p["failure"] = "permission_denied"
			return []execution.NormalizedEvent{event("runtime_failure", p)}, nil
		}
		if strings.TrimSpace(f.Result) == "" || len(f.Result) > maxMessage {
			return bad()
		}
		p["outcome"], p["text"] = "completed", f.Result
		return []execution.NormalizedEvent{event("agent_settled", p)}, nil
	default:
		return bad()
	}
}
func failed(reason string) execution.TerminalResult {
	return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: reason}
}
func (a *Adapter) DecodeTerminal(files execution.TerminalFiles) (execution.TerminalResult, error) {
	if files.TerminationCause != "" && files.TerminationCause != execution.TerminationNone {
		return failed(string(files.TerminationCause)), nil
	}
	if files.ExitCode != 0 {
		return failed("process_failure"), nil
	}
	return failed("protocol_incomplete"), nil
}
func (a *Adapter) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	if r.Files.TerminationCause != "" && r.Files.TerminationCause != execution.TerminationNone && r.Files.TerminationCause != execution.TerminationExitNonzero {
		return failed(string(r.Files.TerminationCause)), nil
	}
	started, settled, sawText := false, false, false
	session, summary, failure := "", "", ""
	pending, seen := map[string]bool{}, map[string]bool{}
	for _, e := range r.Events {
		if e.Type() == "attempt.started" {
			started = true
			settled = false
			sawText = false
			session = ""
			summary = ""
			failure = ""
			pending = map[string]bool{}
			seen = map[string]bool{}
			continue
		}
		if !started || e.Source() != "adapter" {
			continue
		}
		var p struct {
			Session string `json:"session_id"`
			Text    string `json:"text"`
			ID      string `json:"item_id"`
			CallID  string `json:"call_id"`
			Failure string `json:"failure"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(e.NormalizedJSON(), &p) != nil {
			return failed("protocol_invalid"), nil
		}
		if e.Type() == "runtime_session" {
			if session != "" || settled {
				return failed("protocol_invalid"), nil
			}
			session = p.Session
			continue
		}
		if session == "" || p.Session != session || settled {
			return failed("protocol_invalid"), nil
		}
		switch e.Type() {
		case "runtime_failure":
			if failure == "" {
				failure = p.Failure
			}
		case "assistant_message":
			if seen[p.ID] || p.Text == "" {
				return failed("protocol_invalid"), nil
			}
			seen[p.ID] = true
			sawText = true
		case "tool_start":
			if seen[p.CallID] {
				return failed("protocol_invalid"), nil
			}
			seen[p.CallID] = true
			pending[p.CallID] = true
		case "tool_result":
			if !pending[p.CallID] {
				return failed("protocol_invalid"), nil
			}
			delete(pending, p.CallID)
		case "agent_settled":
			if p.Outcome != "completed" || p.Text == "" || len(pending) > 0 {
				return failed("protocol_invalid"), nil
			}
			settled = true
			summary = p.Text
		default:
			return failed("protocol_invalid"), nil
		}
	}
	if failure != "" {
		return failed(failure), nil
	}
	if r.Files.ExitCode != 0 {
		return failed("process_failure"), nil
	}
	if !started || !settled || !sawText {
		return failed("protocol_incomplete"), nil
	}
	return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: summary}, nil
}
