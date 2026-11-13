package opencoderun

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

// DecodeEvent is stateless across runs and replay chunks. Only bounded text and
// identifiers/statuses enter persistence. Native errors, reasoning, tool inputs,
// tool output, paths, snapshots, cost and account metadata are discarded.
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
		e, err := project(line)
		if err != nil {
			return execution.DecodedChunk{}, err
		}
		result.Events = append(result.Events, e)
	}
	return result, nil
}

func allowedTool(tool string) bool {
	switch tool {
	case "bash", "read", "write", "edit", "apply_patch", "glob", "grep", "list", "todowrite", "todoread":
		return true
	}
	return false
}

// Match encoding/json's decoded, case-insensitive field names before typed
// decoding. Only projected fields share this namespace; opaque extensions do
// not gain authority. Nil children are scalar or deliberately opaque values.
type eventFields map[string]eventFields

var partFields = eventFields{
	"id": nil, "sessionID": nil, "messageID": nil, "type": nil,
	"text": nil, "reason": nil, "callID": nil, "tool": nil, "cost": nil,
	"state": {"status": nil}, "time": {"start": nil, "end": nil},
	"tokens": {"input": nil, "output": nil, "reasoning": nil,
		"cache": {"read": nil, "write": nil}},
	"status": nil, "outcome": nil, "error": nil, "is_error": nil,
	"finish": nil, "stop_reason": nil,
}

var frameFields = eventFields{
	"type": nil, "sessionID": nil, "timestamp": nil, "part": partFields,
	"error": {"name": nil}, "status": nil, "outcome": nil,
	"is_error": nil, "finish": nil, "stop_reason": nil,
}

func validateEventFields(raw []byte, fields eventFields) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return execution.ErrEventStreamInvalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return execution.ErrEventStreamInvalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return execution.ErrEventStreamInvalid
		}
		for field, child := range fields {
			if !strings.EqualFold(key, field) {
				continue
			}
			if seen[field] {
				return execution.ErrEventStreamInvalid
			}
			seen[field] = true
			if child != nil && validateEventFields(value, child) != nil {
				return execution.ErrEventStreamInvalid
			}
			break
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return execution.ErrEventStreamInvalid
	}
	return nil // The typed decoder also rejects trailing JSON and scalar types.
}

func project(line []byte) (execution.NormalizedEvent, error) {
	var f struct {
		Type      string          `json:"type"`
		Session   string          `json:"sessionID"`
		Timestamp *int64          `json:"timestamp"`
		Error     json.RawMessage `json:"error"`
		Part      struct {
			ID      string `json:"id"`
			Session string `json:"sessionID"`
			Message string `json:"messageID"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Reason  string `json:"reason"`
			CallID  string `json:"callID"`
			Tool    string `json:"tool"`
			State   struct {
				Status string `json:"status"`
			} `json:"state"`
			Time struct {
				Start *int64 `json:"start"`
				End   *int64 `json:"end"`
			} `json:"time"`
			Cost   *float64 `json:"cost"`
			Tokens struct {
				Input     *int64 `json:"input"`
				Output    *int64 `json:"output"`
				Reasoning *int64 `json:"reasoning"`
				Cache     struct {
					Read  *int64 `json:"read"`
					Write *int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"part"`
	}
	bad := func() (execution.NormalizedEvent, error) {
		return execution.NormalizedEvent{}, execution.ErrEventStreamInvalid
	}
	if validateEventFields(line, frameFields) != nil || json.Unmarshal(line, &f) != nil || !identifier(f.Session) || f.Timestamp == nil || *f.Timestamp < 0 {
		return bad()
	}
	p := map[string]any{"session_id": f.Session}
	if f.Type == "error" {
		var nativeError struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(f.Error, &nativeError) != nil || nativeError.Name == "" {
			return bad()
		}
		p["failure"] = "provider_failure"
		return event("runtime_failure", p), nil
	}
	if len(f.Error) != 0 || !identifier(f.Part.ID) || !identifier(f.Part.Message) || f.Part.Session != f.Session {
		return bad()
	}
	p["item_id"], p["message_id"] = f.Part.ID, f.Part.Message
	switch f.Type {
	case "step_start":
		if f.Part.Type != "step-start" {
			return bad()
		}
		e := event("step_start", p)
		e.ExternalSession = f.Session
		return e, nil
	case "text":
		if f.Part.Type != "text" || strings.TrimSpace(f.Part.Text) == "" || len(f.Part.Text) > maxMessage ||
			f.Part.Time.Start == nil || f.Part.Time.End == nil || *f.Part.Time.Start < 0 || *f.Part.Time.End < *f.Part.Time.Start {
			return bad()
		}
		p["text"] = f.Part.Text
		return event("assistant_message", p), nil
	case "tool_use":
		if f.Part.Type != "tool" || !identifier(f.Part.CallID) || !allowedTool(f.Part.Tool) ||
			(f.Part.State.Status != "completed" && f.Part.State.Status != "error") {
			return bad()
		}
		p["call_id"], p["tool"], p["status"] = f.Part.CallID, f.Part.Tool, f.Part.State.Status
		// Native run JSON reports completed tools only. This observation does
		// not fabricate a tool-start frame or independent verification evidence.
		return event("tool_observation", p), nil
	case "step_finish":
		if f.Part.Type != "step-finish" || f.Part.Cost == nil || *f.Part.Cost < 0 {
			return bad()
		}
		for _, counter := range []*int64{f.Part.Tokens.Input, f.Part.Tokens.Output, f.Part.Tokens.Reasoning, f.Part.Tokens.Cache.Read, f.Part.Tokens.Cache.Write} {
			if counter == nil || *counter < 0 {
				return bad()
			}
		}
		// Additive non-authoritative metadata may evolve. New terminal authority
		// needs semantic review before it can affect completion.
		var raw map[string]json.RawMessage
		var part map[string]json.RawMessage
		_ = json.Unmarshal(line, &raw)
		// The typed decoder accepts aliases for part as well as its fields.
		for key, value := range raw {
			if strings.EqualFold(key, "part") {
				_ = json.Unmarshal(value, &part)
			}
		}
		for _, object := range []map[string]json.RawMessage{raw, part} {
			for key := range object {
				for _, authority := range []string{"status", "outcome", "error", "is_error", "finish", "stop_reason"} {
					if strings.EqualFold(key, authority) {
						return bad()
					}
				}
			}
		}
		switch f.Part.Reason {
		case "stop", "tool-calls":
			p["reason"] = f.Part.Reason
			return event("step_finish", p), nil
		case "length", "content-filter", "error", "other", "unknown":
			p["failure"] = "native_incomplete"
			return event("runtime_failure", p), nil
		default:
			return bad()
		}
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

// Complete pairs every observed step with the same session/message and requires
// final text, native stop, and trusted zero exit. Session idle alone has no
// terminal JSON frame in this protocol and cannot establish success.
func (a *Adapter) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	if r.Files.TerminationCause != "" && r.Files.TerminationCause != execution.TerminationNone && r.Files.TerminationCause != execution.TerminationExitNonzero {
		return failed(string(r.Files.TerminationCause)), nil
	}
	started, settled := false, false
	session, message, summary, failure := "", "", "", ""
	tools := 0
	seen, calls, messages := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range r.Events {
		if e.Type() == "attempt.started" {
			started, settled = true, false
			session, message, summary, failure, tools = "", "", "", "", 0
			seen, calls, messages = map[string]bool{}, map[string]bool{}, map[string]bool{}
			continue
		}
		if !started || e.Source() != "adapter" {
			continue
		}
		var p struct {
			Session string `json:"session_id"`
			Message string `json:"message_id"`
			ID      string `json:"item_id"`
			CallID  string `json:"call_id"`
			Text    string `json:"text"`
			Reason  string `json:"reason"`
			Failure string `json:"failure"`
		}
		if json.Unmarshal(e.NormalizedJSON(), &p) != nil || !identifier(p.Session) || settled {
			return failed("protocol_invalid"), nil
		}
		if session == "" {
			session = p.Session
		}
		if p.Session != session {
			return failed("protocol_invalid"), nil
		}
		if e.Type() == "runtime_failure" {
			failure = p.Failure
			if failure == "" {
				return failed("protocol_invalid"), nil
			}
			continue
		}
		if !identifier(p.ID) || seen[p.ID] || !identifier(p.Message) {
			return failed("protocol_invalid"), nil
		}
		seen[p.ID] = true
		if e.Type() == "step_start" {
			if message != "" || messages[p.Message] {
				return failed("protocol_invalid"), nil
			}
			message, summary, tools = p.Message, "", 0
			messages[p.Message] = true
			continue
		}
		if message == "" || p.Message != message {
			return failed("protocol_invalid"), nil
		}
		switch e.Type() {
		case "assistant_message":
			if strings.TrimSpace(p.Text) == "" || len(summary)+len(p.Text) > maxMessage {
				return failed("protocol_invalid"), nil
			}
			summary += p.Text
		case "tool_observation":
			if !identifier(p.CallID) || calls[p.CallID] {
				return failed("protocol_invalid"), nil
			}
			calls[p.CallID] = true
			tools++
		case "step_finish":
			switch p.Reason {
			case "stop":
				if summary == "" || tools != 0 {
					return failed("protocol_invalid"), nil
				}
				settled = true
			case "tool-calls":
				if tools == 0 {
					return failed("protocol_invalid"), nil
				}
			default:
				return failed("protocol_invalid"), nil
			}
			message = ""
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
	if !started || !settled {
		return failed("protocol_incomplete"), nil
	}
	return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: summary}, nil
}
