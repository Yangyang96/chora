package claudeprint

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestCompletionRequiresNativeSuccessAndTrustedExit(t *testing.T) {
	a := &Adapter{}
	for _, tc := range []struct {
		name, stream, want string
		exit               int
	}{
		{"success", successStream, "", 0},
		{"nonzero", successStream, "process_failure", 1},
		{"empty", "", "protocol_incomplete", 0},
		{"missing assistant", initStream + resultStream, "protocol_incomplete", 0},
		{"missing result", initStream + messageStream, "protocol_incomplete", 0},
		{"reordered", resultStream + initStream + messageStream, "protocol_invalid", 0},
		{"duplicate result", successStream + resultStream, "protocol_invalid", 0},
		{"duplicate message", initStream + messageStream + messageStream + resultStream, "protocol_invalid", 0},
		{"wrong session", initStream + strings.ReplaceAll(messageStream+resultStream, "session-1", "session-2"), "protocol_invalid", 0},
		{"native auth error", initStream + `{"type":"assistant","session_id":"session-1","error":"authentication_failed","is_api_error_message":true,"message":{"content":[{"type":"text","text":"secret diagnostic"}]}}` + "\n" + strings.Replace(resultStream, `"is_error":false`, `"is_error":true`, 1), "authentication_rejected", 1},
		{"success subtype with error", initStream + messageStream + strings.Replace(resultStream, `"is_error":false`, `"is_error":true`, 1), "provider_failure", 0},
		{"permission denied", initStream + messageStream + strings.Replace(resultStream, `"permission_denials":[]`, `"permission_denials":[{"tool_name":"Write","tool_input":{"file_path":"private"}}]`, 1), "permission_denied", 0},
		{"max turns", initStream + messageStream + strings.Replace(resultStream, `"subtype":"success"`, `"subtype":"error_max_turns"`, 1), "provider_failure", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(tc.stream), EOF: true})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(d)
			if strings.Contains(string(raw), "secret diagnostic") || strings.Contains(string(raw), "private") {
				t.Fatal("diagnostics persisted")
			}
			result, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{ExitCode: tc.exit}})
			if err != nil || result.FailureReason != tc.want {
				t.Fatalf("kind=%s failure=%s err=%v", result.Kind, result.FailureReason, err)
			}
		})
	}
	d, _ := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream), EOF: true})
	result, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: execution.TerminalFiles{TerminationCause: execution.TerminationDeadlineExceeded}})
	if result.Kind != execution.TerminalFailed {
		t.Fatal("success overrode deadline")
	}
}

func TestFramingPolicyEvidenceAndSessionIndependence(t *testing.T) {
	a := &Adapter{}
	for _, bad := range []string{
		"{\n", "{}\n", strings.TrimSuffix(successStream, "\n"), strings.Repeat("x", maxFrame+1),
		strings.Replace(initStream, `"mcp_servers":[]`, `"mcp_servers":[{}]`, 1),
		strings.Replace(initStream, `"plugins":[]`, `"plugins":null`, 1),
		strings.Replace(initStream, `"skills":[]`, `"skills":[{}]`, 1),
		strings.Replace(initStream, `"Read"`, `"Agent"`, 1),
		strings.Replace(resultStream, `"is_error":false,`, "", 1),
		strings.Replace(messageStream, `"type":"assistant"`, `"type":"assistant","parent_tool_use_id":"parent"`, 1),
	} {
		if _, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(bad), EOF: true}); err == nil {
			t.Fatal("invalid protocol accepted")
		}
	}
	for split := 0; split < len(successStream); split++ {
		first, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[:split])})
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(successStream[first.ConsumedBytes:]), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Complete(execution.CompletionRequest{Events: persisted(t, append(first.Events, second.Events...))})
		if err != nil || result.Kind != execution.TerminalReviewReady {
			t.Fatal(split, result, err)
		}
	}
	result, _ := a.Complete(execution.CompletionRequest{Events: persisted(t, nil)})
	if result.Kind != execution.TerminalFailed {
		t.Fatal("cross-session completion")
	}
}

func TestFileToolProjectionDropsInputsAndResults(t *testing.T) {
	a := &Adapter{}
	tool := `{"type":"assistant","session_id":"session-1","message":{"id":"tool-message","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{"file_path":"private-input"}}]}}` + "\n"
	done := `{"type":"user","session_id":"session-1","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"private-output"}]}}` + "\n"
	d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(initStream + tool + done + messageStream + resultStream), EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("tool content leaked")
	}
	result, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
	if err != nil || result.Kind != execution.TerminalReviewReady {
		t.Fatal(result, err)
	}
	d, _ = a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(initStream + tool + messageStream + resultStream), EOF: true})
	result, _ = a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
	if result.Kind != execution.TerminalFailed {
		t.Fatal("unfinished tool accepted")
	}
}
