package hermes

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/execution"
)

const maxFrame = 256 * 1024
const maxText = 64 * 1024

func identifier(s string) bool {
	return s != "" && len(s) <= 128 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") == ""
}
func event(kind string, p map[string]any) execution.NormalizedEvent {
	p["event_type"] = kind
	raw, _ := json.Marshal(p)
	return execution.NormalizedEvent{Type: kind, NormalizedJSON: raw}
}

// DecodeEvent is stateless across stream chunks. Only text, opaque session/call
// identifiers and tool names survive; native errors, input/output and stderr do
// not enter persisted events. Unknown metadata fields are safely ignored.
func (*Adapter) DecodeEvent(c execution.EventChunk) (execution.DecodedChunk, error) {
	if c.Stream == execution.StreamStderr {
		return execution.DecodedChunk{ConsumedBytes: len(c.Data)}, nil
	}
	if c.Stream != execution.StreamStdout {
		return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
	}
	end := bytes.LastIndexByte(c.Data, '\n') + 1
	if c.EOF && end != len(c.Data) || len(c.Data)-end > maxFrame {
		return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
	}
	r := execution.DecodedChunk{ConsumedBytes: end}
	for _, line := range bytes.Split(c.Data[:end], []byte{'\n'})[:bytes.Count(c.Data[:end], []byte{'\n'})] {
		if len(line) == 0 || len(line) > maxFrame || !utf8.Valid(line) {
			return execution.DecodedChunk{}, execution.ErrEventStreamInvalid
		}
		e, err := project(line)
		if err != nil {
			return execution.DecodedChunk{}, err
		}
		r.Events = append(r.Events, e)
	}
	return r, nil
}

func project(raw []byte) (execution.NormalizedEvent, error) {
	var f struct {
		Type, Subtype, Text, Name, Error string
		Session                          string `json:"session_id"`
		CallID                           string `json:"tool_call_id"`
		ExitCode                         *int   `json:"exit_code"`
		IsError                          *bool  `json:"is_error"`
		ReplayGap                        bool   `json:"replay_gap"`
	}
	bad := func() (execution.NormalizedEvent, error) {
		return execution.NormalizedEvent{}, execution.ErrEventStreamInvalid
	}
	// Use the same case folding as encoding/json's struct-field matching.
	// Token decodes escaped keys before comparison. Opaque metadata retains its
	// own namespace and never contributes fields to this envelope.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return bad()
	}
	keys := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return bad()
		}
		for _, field := range []string{"type", "subtype", "text", "name", "error", "session_id", "tool_call_id", "exit_code", "is_error", "replay_gap"} {
			if strings.EqualFold(key, field) {
				key = field
				break
			}
		}
		if keys[key] {
			return bad()
		}
		keys[key] = true
		var discard json.RawMessage
		if decoder.Decode(&discard) != nil {
			return bad()
		}
	}
	if json.Unmarshal(raw, &f) != nil {
		return bad()
	}
	if f.Session != "" && !identifier(f.Session) {
		return bad()
	}
	switch f.Type {
	case "system":
		// The gateway emitter may not know its assigned session at init time.
		if f.Subtype != "init" || f.Session != "" && !identifier(f.Session) {
			return bad()
		}
		return event("runtime_session", map[string]any{"session_id": f.Session}), nil
	case "text":
		if f.Text == "" || len(f.Text) > maxText {
			return bad()
		}
		return event("assistant_message", map[string]any{"text": f.Text, "session_id": f.Session}), nil
	case "tool_use", "tool_result":
		if !identifier(f.Name) || f.CallID != "" && !identifier(f.CallID) {
			return bad()
		}
		p := map[string]any{"tool_name": f.Name, "call_id": f.CallID, "session_id": f.Session}
		if f.Type == "tool_use" {
			return event("tool_start", p), nil
		}
		if f.IsError == nil {
			return bad()
		}
		p["is_error"] = *f.IsError
		return event("tool_result", p), nil
	case "result":
		if !identifier(f.Session) || f.ExitCode == nil || f.ReplayGap {
			return bad()
		}
		p := map[string]any{"session_id": f.Session}
		if *f.ExitCode != 0 || f.Error != "" {
			p["failure"] = "provider_failure"
			return event("runtime_failure", p), nil
		}
		if strings.TrimSpace(f.Text) == "" || len(f.Text) > maxText {
			return bad()
		}
		p["text"], p["outcome"] = f.Text, "completed"
		return event("agent_settled", p), nil
	}
	return bad()
}

func failed(reason string) execution.TerminalResult {
	return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: reason}
}
func (*Adapter) DecodeTerminal(f execution.TerminalFiles) (execution.TerminalResult, error) {
	if f.TerminationCause != "" && f.TerminationCause != execution.TerminationNone {
		return failed(string(f.TerminationCause)), nil
	}
	if f.ExitCode != 0 {
		return failed("process_failure"), nil
	}
	return failed("protocol_incomplete"), nil
}

// Complete evaluates only the current attempt's trusted adapter events. Native
// exit zero alone, a result file, or an unpaired terminal can never yield success.
func (*Adapter) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	if r.Files.TerminationCause != "" && r.Files.TerminationCause != execution.TerminationNone && r.Files.TerminationCause != execution.TerminationExitNonzero {
		return failed(string(r.Files.TerminationCause)), nil
	}
	started, initialized, terminal := false, false, false
	session, text, summary, failure := "", "", "", ""
	pending, seen := map[string]string{}, map[string]bool{}
	for _, e := range r.Events {
		if e.Type() == "attempt.started" {
			started, initialized, terminal = true, false, false
			session, text, summary, failure = "", "", "", ""
			pending, seen = map[string]string{}, map[string]bool{}
			continue
		}
		if !started || e.Source() != "adapter" {
			continue
		}
		var p struct {
			Session string `json:"session_id"`
			Text    string `json:"text"`
			Name    string `json:"tool_name"`
			CallID  string `json:"call_id"`
			Failure string `json:"failure"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(e.NormalizedJSON(), &p) != nil || terminal {
			return failed("protocol_invalid"), nil
		}
		if e.Type() == "runtime_session" {
			if initialized || p.Session != "" && !identifier(p.Session) {
				return failed("protocol_invalid"), nil
			}
			initialized, session = true, p.Session
			continue
		}
		if !initialized {
			return failed("protocol_invalid"), nil
		}
		if p.Session != "" {
			if !identifier(p.Session) || session != "" && p.Session != session {
				return failed("protocol_invalid"), nil
			}
			session = p.Session
		}
		switch e.Type() {
		case "assistant_message":
			text += p.Text
			if p.Text == "" || len(text) > maxText {
				return failed("protocol_invalid"), nil
			}
		case "tool_start", "tool_result":
			if !identifier(p.Name) || p.CallID != "" && !identifier(p.CallID) {
				return failed("protocol_invalid"), nil
			}
			key := "id:" + p.CallID
			if p.CallID == "" {
				key = "name:" + p.Name
			}
			if e.Type() == "tool_start" {
				// Name-only calls can repeat sequentially, never overlap ambiguously.
				if pending[key] != "" || p.CallID != "" && seen[key] {
					return failed("protocol_invalid"), nil
				}
				pending[key], seen[key] = p.Name, true
			} else {
				if pending[key] != p.Name {
					return failed("protocol_invalid"), nil
				}
				delete(pending, key)
			}
			text = "" // the final assistant turn must pair with the terminal text
		case "agent_settled", "runtime_failure":
			if !identifier(p.Session) || session != "" && session != p.Session {
				return failed("protocol_invalid"), nil
			}
			terminal = true
			if e.Type() == "runtime_failure" {
				failure = "provider_failure"
				continue
			}
			if len(pending) > 0 || p.Outcome != "completed" || strings.TrimSpace(text) == "" || text != p.Text {
				return failed("protocol_invalid"), nil
			}
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
	if !terminal || summary == "" {
		return failed("protocol_incomplete"), nil
	}
	return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: summary}, nil
}
