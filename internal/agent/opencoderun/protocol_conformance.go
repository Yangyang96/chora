package opencoderun

import (
	"errors"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const protocolStart = `{"type":"step_start","timestamp":1,"sessionID":"session-1","part":{"id":"part-1","sessionID":"session-1","messageID":"message-1","type":"step-start"}}` + "\n"
const protocolText = `{"type":"text","timestamp":2,"sessionID":"session-1","part":{"id":"part-2","sessionID":"session-1","messageID":"message-1","type":"text","text":"Complete synthetic finding.","time":{"start":1,"end":2}}}` + "\n"
const protocolFinish = `{"type":"step_finish","timestamp":3,"sessionID":"session-1","part":{"id":"part-3","sessionID":"session-1","messageID":"message-1","type":"step-finish","reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
const protocolSuccess = protocolStart + protocolText + protocolFinish
const protocolTool = `{"type":"tool_use","timestamp":2,"sessionID":"session-1","part":{"id":"tool-1","sessionID":"session-1","messageID":"message-1","type":"tool","tool":"read","callID":"call-1","state":{"status":"completed","input":{"path":"synthetic"},"output":"synthetic"}}}` + "\n"
const protocolError = `{"type":"error","timestamp":1,"sessionID":"session-1","error":{"name":"APIError","data":{"message":"synthetic authentication failure"}}}` + "\n"

type protocolCase struct {
	Stream, Reason string
	ExitCode       int
	Cause          execution.TerminationCause
}

// Fixed synthetic inputs exercise partial adapter semantics only. Native source
// mapping, full turn/auth/tool/exit behavior require independent signed evidence.
func protocolCases() []protocolCase {
	tools := protocolStart + protocolTool + strings.Replace(protocolFinish, `"reason":"stop"`, `"reason":"tool-calls"`, 1) + strings.ReplaceAll(strings.ReplaceAll(protocolSuccess, "part-", "final-"), "message-1", "message-2")
	return []protocolCase{
		{Stream: protocolSuccess},
		{Stream: tools},
		{Stream: strings.Replace(tools, `"status":"completed"`, `"status":"error"`, 1)},
		{Stream: protocolSuccess, ExitCode: 1, Reason: "process_failure"},
		{Stream: protocolSuccess, Cause: execution.TerminationDeadlineExceeded, Reason: string(execution.TerminationDeadlineExceeded)},
		{Stream: protocolError, ExitCode: 1, Reason: "provider_failure"},
		{Stream: protocolStart + protocolError + protocolText + protocolFinish, Reason: "provider_failure"},
		{Stream: protocolSuccess + protocolFinish, Reason: "protocol_invalid"},
		{Stream: protocolStart + protocolText, Reason: "protocol_incomplete"},
		{Stream: protocolFinish, Reason: "protocol_invalid"},
		{Stream: protocolStart + strings.ReplaceAll(protocolText+protocolFinish, "message-1", "message-2"), Reason: "protocol_invalid"},
		{Stream: strings.Replace(protocolSuccess, `"reason":"stop"`, `"reason":"new-terminal"`, 1), Reason: "protocol_invalid"},
		{Stream: strings.Replace(protocolSuccess, `"reason":"stop"`, `"reason":"stop","status":"failed"`, 1), Reason: "protocol_invalid"},
		{Stream: strings.Replace(protocolSuccess, `"reason":"stop"`, `"reason":"length"`, 1), Reason: "native_incomplete"},
		{Stream: protocolStart + strings.Replace(protocolFinish, `"reason":"stop"`, `"reason":"tool-calls"`, 1), Reason: "protocol_invalid"},
		{Stream: strings.Replace(protocolSuccess, `"input":1`, `"input":-1`, 1), Reason: "protocol_invalid"},
		{Stream: strings.TrimSuffix(protocolSuccess, "\n"), Reason: "protocol_invalid"},
		{Stream: strings.Replace(protocolSuccess, `"tokens":{`, `"metadata":{"future":"discarded"},"tokens":{`, 1)},
		{Stream: strings.Replace(tools, `"tool":"read"`, `"tool":"mcp_unknown"`, 1), Reason: "protocol_invalid"},
	}
}

func protocolCorpusDigest() [32]byte {
	return hash(struct {
		Protocol, Validator string
		Cases               []protocolCase
	}{protocolVersion, "opencode.adapter-conformance.v1", protocolCases()})
}

func validateProtocolConformance() error {
	for _, c := range protocolCases() {
		a := &Adapter{}
		decoded, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(c.Stream), EOF: true})
		if err != nil {
			if c.Reason == "protocol_invalid" && errors.Is(err, execution.ErrEventStreamInvalid) {
				continue
			}
			return ErrProtocolReview
		}
		events := []domain.RunEvent{}
		now, run := time.Now().UTC(), domain.NewRunID()
		for i, e := range append([]execution.NormalizedEvent{{Type: "attempt.started", NormalizedJSON: []byte(`{}`)}}, decoded.Events...) {
			source := "adapter"
			if i == 0 {
				source = "app"
			}
			event, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: run, Sequence: int64(i + 1), Type: e.Type, Source: source, OccurredAt: now, RecordedAt: now, NormalizedJSON: e.NormalizedJSON})
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		result, err := a.Complete(execution.CompletionRequest{Events: events, Files: execution.TerminalFiles{ExitCode: c.ExitCode, TerminationCause: c.Cause}})
		if err != nil || result.FailureReason != c.Reason || (c.Reason == "" && result.Kind != execution.TerminalReviewReady) {
			return ErrProtocolReview
		}
	}
	return nil
}
