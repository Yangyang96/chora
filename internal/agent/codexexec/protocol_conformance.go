package codexexec

import (
	"errors"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

// These public synthetic streams test the adapter, not native execution. The
// signed review must additionally bind the reviewed executable's source to the
// protocol. Never accept caller-provided transcripts as an authority.
const protocolSuccess = `{"type":"thread.started","thread_id":"conformance"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Synthetic answer."}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}
`

type protocolCase struct{ Stream, Reason string }

func protocolCases() []protocolCase {
	return []protocolCase{
		{protocolSuccess, ""},
		{strings.Replace(protocolSuccess, `"usage":{`, `"future_metadata":true,"usage":{`, 1), ""},
		{strings.Replace(protocolSuccess, `"output_tokens":1`, `"output_tokens":1,"future_counter":{}`, 1), ""},
		{protocolSuccess + `{"type":"turn.failed","error":{"message":"conformance failure"}}` + "\n", "provider_failure"},
		{strings.Replace(protocolSuccess, `"cached_input_tokens":0,`, "", 1), "protocol_invalid"},
		{strings.Replace(protocolSuccess, `"cached_input_tokens":0`, `"cached_input_tokens":null`, 1), "protocol_invalid"},
		{strings.Replace(protocolSuccess, `"turn.completed"`, `"turn.unknown_terminal"`, 1), "protocol_invalid"},
		{strings.Replace(protocolSuccess, `"usage":{`, `"status":"failed","usage":{`, 1), "protocol_invalid"},
		{protocolSuccess + `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}` + "\n", "protocol_invalid"},
		{strings.Replace(protocolSuccess, `{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Synthetic answer."}}`, `{"type":"item.started","item":{"id":"tool","type":"command_execution","status":"in_progress"}}`+"\n"+`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Synthetic answer."}}`, 1), "protocol_invalid"},
		{strings.Replace(protocolSuccess, `{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Synthetic answer."}}`, `{"type":"item.completed","item":{"id":"tool","type":"command_execution","status":"completed","exit_code":0}}`+"\n"+`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"Synthetic answer."}}`, 1), "protocol_invalid"},
		{`{"type":"item.completed","item":{"id":"tool","type":"command_execution","status":"unknown","exit_code":0}}` + "\n", "protocol_invalid"},
	}
}

func protocolCorpusDigest() [32]byte {
	return hash(struct {
		Protocol, Validator string
		Cases               []protocolCase
	}{protocolVersion, "adapter-conformance.v1", protocolCases()})
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
		result, err := a.Complete(execution.CompletionRequest{Events: events})
		if err != nil || result.FailureReason != c.Reason || (c.Reason == "" && result.Kind != execution.TerminalReviewReady) {
			return ErrProtocolReview
		}
	}
	return nil
}
