package opencoderun

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

const errorStream = `{"type":"error","timestamp":1,"sessionID":"session-1","error":{"name":"APIError","data":{"message":"private-diagnostic"}}}` + "\n"
const toolStream = `{"type":"tool_use","timestamp":2,"sessionID":"session-1","part":{"id":"tool-1","sessionID":"session-1","messageID":"message-1","type":"tool","tool":"read","callID":"call-1","state":{"status":"completed","input":{"path":"private-input"},"output":"private-output"}}}` + "\n"

func TestCompletionRequiresPairedFinalStepAndTrustedExit(t *testing.T) {
	a := &Adapter{}
	for _, tc := range []struct {
		name, stream, failure string
		exit                  int
	}{
		{"success", successStream, "", 0},
		{"exit failure", successStream, "process_failure", 1},
		{"empty", "", "protocol_incomplete", 0},
		{"no text", initStream + resultStream, "protocol_invalid", 0},
		{"no finish", initStream + messageStream, "protocol_incomplete", 0},
		{"no start", messageStream + resultStream, "protocol_invalid", 0},
		{"duplicate start", initStream + successStream, "protocol_invalid", 0},
		{"duplicate finish", successStream + resultStream, "protocol_invalid", 0},
		{"duplicate text", initStream + messageStream + messageStream + resultStream, "protocol_invalid", 0},
		{"wrong session", initStream + strings.ReplaceAll(messageStream+resultStream, "session-1", "session-2"), "protocol_invalid", 0},
		{"wrong message", initStream + strings.ReplaceAll(messageStream+resultStream, "message-1", "message-2"), "protocol_invalid", 0},
		{"error before start", errorStream, "provider_failure", 1},
		{"error then stop", initStream + errorStream + messageStream + resultStream, "provider_failure", 0},
		{"length limit", initStream + messageStream + strings.Replace(resultStream, `"reason":"stop"`, `"reason":"length"`, 1), "native_incomplete", 0},
		{"unpaired tool finish", initStream + strings.Replace(resultStream, `"reason":"stop"`, `"reason":"tool-calls"`, 1), "protocol_invalid", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(tc.stream), EOF: true})
			if err != nil {
				t.Fatal(err)
			}
			r, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{ExitCode: tc.exit}})
			if err != nil || r.FailureReason != tc.failure || (tc.failure == "" && r.Kind != execution.TerminalReviewReady) {
				t.Fatal(r, err)
			}
		})
	}
	d, _ := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream), EOF: true})
	r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{TerminationCause: execution.TerminationDeadlineExceeded}})
	if r.Kind != execution.TerminalFailed {
		t.Fatal("deadline overridden", r)
	}
	if r, _ := a.DecodeTerminal(execution.TerminalFiles{}); r.Kind != execution.TerminalFailed {
		t.Fatal("exit alone completed")
	}
}

func TestFramingCriticalFieldsAndForwardMetadata(t *testing.T) {
	a := &Adapter{}
	for _, stream := range []string{
		"{\n", "{}\n", "\xff\n", strings.TrimSuffix(successStream, "\n"), strings.Repeat("x", maxFrame+1),
		strings.Replace(initStream, `"step_start"`, `"new_terminal"`, 1),
		strings.Replace(resultStream, `"reason":"stop"`, `"reason":"new-reason"`, 1),
		strings.Replace(resultStream, `"reason":"stop"`, `"reason":"stop","status":"error"`, 1),
		strings.Replace(resultStream, `"timestamp":3`, `"timestamp":3,"outcome":"failed"`, 1),
		strings.Replace(resultStream, `"input":1`, `"input":-1`, 1),
		strings.Replace(resultStream, `"output":1`, `"output":1.5`, 1),
		strings.Replace(resultStream, `"reasoning":0,`, "", 1),
		strings.Replace(resultStream, `"cost":0`, `"cost":null`, 1),
		strings.Replace(messageStream, `"end":2`, `"end":null`, 1),
		strings.Replace(toolStream, `"status":"completed"`, `"status":"running"`, 1),
		strings.Replace(toolStream, `"tool":"read"`, `"tool":"skill"`, 1),
		strings.Replace(toolStream, `"tool":"read"`, `"tool":"task"`, 1),
		strings.Replace(toolStream, `"tool":"read"`, `"tool":"mcp_tool"`, 1),
	} {
		if _, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true}); err == nil {
			t.Fatal("invalid stream accepted", stream)
		}
	}
	for split := 0; split < len(successStream); split++ {
		first, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[:split])})
		if err != nil {
			t.Fatal(err)
		}
		last, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[first.ConsumedBytes:]), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		r, err := a.Complete(execution.CompletionRequest{Events: persisted(t, append(first.Events, last.Events...))})
		if err != nil || r.Kind != execution.TerminalReviewReady {
			t.Fatal(split, r, err)
		}
	}
	stream := strings.Replace(successStream, `"tokens":{`, `"metadata":{"future":"private-canary"},"tokens":{`, 1)
	d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true})
	if err != nil {
		t.Fatal("additive metadata rejected", err)
	}
	r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
	if r.Kind != execution.TerminalReviewReady {
		t.Fatal(r)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "private-canary") {
		t.Fatal("metadata leaked")
	}
	if r, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, nil)}); r.Kind != execution.TerminalFailed {
		t.Fatal("state leaked between runs")
	}
}

func TestToolObservationsAndDiagnosticRedaction(t *testing.T) {
	a := &Adapter{}
	toolFinish := strings.Replace(resultStream, `"reason":"stop"`, `"reason":"tool-calls"`, 1)
	final := strings.ReplaceAll(strings.ReplaceAll(successStream, "message-1", "message-2"), "part-", "final-")
	for _, status := range []string{"completed", "error"} {
		tool := strings.Replace(toolStream, `"status":"completed"`, `"status":"`+status+`"`, 1)
		stream := initStream + tool + toolFinish + final
		d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "private-") {
			t.Fatal("tool contents leaked")
		}
		r, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
		if err != nil || r.Kind != execution.TerminalReviewReady {
			t.Fatal(r, err)
		}
	}
	d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(errorStream), EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "private-diagnostic") {
		t.Fatal("error leaked")
	}
	if d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStderr, Data: []byte("private-stderr")}); err != nil || len(d.Events) != 0 {
		t.Fatal(d, err)
	}
}
