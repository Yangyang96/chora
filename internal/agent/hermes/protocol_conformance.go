package hermes

import (
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const initFrame = `{"type":"system","subtype":"init","session_id":""}` + "\n"
const textFrame = `{"type":"text","text":"bounded result"}` + "\n"
const resultFrame = `{"type":"result","session_id":"session-1","exit_code":0,"text":"bounded result"}` + "\n"
const toolFrame = `{"type":"tool_use","name":"read_file","tool_call_id":"call-1","input":{"path":"private-input"}}` + "\n"
const toolResultFrame = `{"type":"tool_result","name":"read_file","tool_call_id":"call-1","output":"private-output","is_error":false}` + "\n"

type replay struct {
	Name, Stream, Failure string
	Exit                  int
	Invalid               bool
}

func protocolCorpus() []replay {
	return []replay{
		{Name: "success", Stream: initFrame + textFrame + resultFrame},
		{Name: "tool", Stream: initFrame + toolFrame + toolResultFrame + textFrame + resultFrame},
		{Name: "name_only_tools", Stream: initFrame + strings.ReplaceAll(toolFrame+toolResultFrame+toolFrame+toolResultFrame, `"tool_call_id":"call-1",`, "") + textFrame + resultFrame},
		{Name: "forward_metadata", Stream: initFrame + strings.Replace(textFrame, `"type":"text"`, `"type":"text","future_usage":{"count":1}`, 1) + resultFrame},
		{Name: "nonzero", Stream: initFrame + textFrame + resultFrame, Exit: 1, Failure: "process_failure"},
		{Name: "empty", Failure: "protocol_incomplete"},
		{Name: "missing_result", Stream: initFrame + textFrame, Failure: "protocol_incomplete"},
		{Name: "missing_text", Stream: initFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "reordered", Stream: resultFrame + initFrame, Failure: "protocol_invalid"},
		{Name: "duplicate_terminal", Stream: initFrame + textFrame + resultFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "mismatched_text", Stream: initFrame + textFrame + strings.Replace(resultFrame, "bounded result", "other result", 1), Failure: "protocol_invalid"},
		{Name: "wrong_session", Stream: strings.Replace(initFrame, `"session_id":""`, `"session_id":"other"`, 1) + textFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "text_session_drift", Stream: initFrame + strings.Replace(textFrame, `"type":"text"`, `"type":"text","session_id":"other"`, 1) + resultFrame, Failure: "protocol_invalid"},
		{Name: "unfinished_tool", Stream: initFrame + toolFrame + textFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "duplicate_tool", Stream: initFrame + toolFrame + toolFrame + toolResultFrame + textFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "unpaired_tool", Stream: initFrame + toolResultFrame + textFrame + resultFrame, Failure: "protocol_invalid"},
		{Name: "native_failure", Stream: initFrame + strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":1,"error":"private-error"`, 1), Failure: "provider_failure"},
		{Name: "error_with_zero", Stream: initFrame + textFrame + strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":0,"error":"private-error"`, 1), Failure: "provider_failure"},
		{Name: "truncated", Stream: strings.TrimSuffix(resultFrame, "\n"), Invalid: true},
		{Name: "malformed", Stream: "{\n", Invalid: true},
		{Name: "unknown_event", Stream: "{\"type\":\"permission_request\"}\n", Invalid: true},
		{Name: "missing_exit", Stream: strings.Replace(resultFrame, `"exit_code":0,`, "", 1), Invalid: true},
		{Name: "duplicate_envelope", Stream: strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":1,"exit_code":0`, 1), Invalid: true},
		{Name: "error_alias", Stream: strings.Replace(resultFrame, `"exit_code":0`, `"error":"private-error","Error":"","exit_code":0`, 1), Invalid: true},
		{Name: "exit_alias", Stream: strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":1,"EXIT_CODE":0`, 1), Invalid: true},
		{Name: "type_alias", Stream: strings.Replace(resultFrame, `"type":"result"`, `"type":"permission_request","Type":"result"`, 1), Invalid: true},
		{Name: "session_alias", Stream: strings.Replace(resultFrame, `"session_id":"session-1"`, `"session_id":"other","SESSION_ID":"session-1"`, 1), Invalid: true},
		{Name: "escaped_alias", Stream: strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":1,"\u0045XIT_CODE":0`, 1), Invalid: true},
		{Name: "unicode_alias", Stream: strings.Replace(resultFrame, `"session_id":"session-1"`, `"session_id":"other","ſeſſion_id":"session-1"`, 1), Invalid: true},
		{Name: "missing_tool_status", Stream: strings.Replace(toolResultFrame, `,"is_error":false`, "", 1), Invalid: true},
		{Name: "replay_gap", Stream: strings.Replace(resultFrame, `"exit_code":0`, `"exit_code":0,"replay_gap":true`, 1), Invalid: true},
	}
}
func protocolCorpusDigest() [32]byte { return hash(protocolCorpus()) }

// replayEvents uses fixed synthetic IDs exclusively for in-process conformance.
func replayEvents(events []execution.NormalizedEvent) ([]domain.RunEvent, error) {
	all := append([]execution.NormalizedEvent{{Type: "attempt.started", NormalizedJSON: []byte(`{}`)}}, events...)
	run, _ := domain.ParseRunID("run_018f0c4a-1a2b-7c3d-8e4f-1234567890ab")
	var out []domain.RunEvent
	for n, e := range all {
		id, _ := domain.ParseEventID(fmt.Sprintf("event_018f0c4a-1a2b-7c3d-8e4f-%012d", n+1))
		source := "adapter"
		if n == 0 {
			source = "app"
		}
		tm := time.Unix(1, 0).UTC()
		persisted, err := domain.NewRunEvent(domain.RunEventParams{ID: id, RunID: run, Sequence: int64(n + 1), Type: e.Type, Source: source, OccurredAt: tm, RecordedAt: tm, NormalizedJSON: e.NormalizedJSON})
		if err != nil {
			return nil, err
		}
		out = append(out, persisted)
	}
	return out, nil
}
func validateProtocolConformance() error {
	a := &Adapter{}
	for _, r := range protocolCorpus() {
		d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(r.Stream), EOF: true})
		if r.Invalid {
			if err == nil {
				return ErrProtocolReview
			}
			continue
		}
		if err != nil {
			return err
		}
		events, err := replayEvents(d.Events)
		if err != nil {
			return err
		}
		result, err := a.Complete(execution.CompletionRequest{Events: events, Files: execution.TerminalFiles{ExitCode: r.Exit}})
		if err != nil || result.FailureReason != r.Failure || (r.Failure == "" && result.Kind != execution.TerminalReviewReady) {
			return ErrProtocolReview
		}
	}
	return nil
}
