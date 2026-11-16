package hermes

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestFixedProtocolConformance(t *testing.T) {
	if err := validateProtocolConformance(); err != nil {
		t.Fatal(err)
	}
	for _, r := range protocolCorpus() {
		t.Run(r.Name, func(t *testing.T) {
			a := &Adapter{}
			d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(r.Stream), EOF: true})
			if r.Invalid {
				if err == nil {
					t.Fatal("invalid accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			events, err := replayEvents(d.Events)
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.Complete(execution.CompletionRequest{Events: events, Files: execution.TerminalFiles{ExitCode: r.Exit}})
			if err != nil || result.FailureReason != r.Failure {
				t.Fatal(result, err)
			}
			raw, _ := json.Marshal(d.Events)
			if strings.Contains(string(raw), "private-") {
				t.Fatal("private data persisted")
			}
		})
	}
}

func TestFramingConcurrentChunksAndStopPriority(t *testing.T) {
	a := &Adapter{}
	stream := initFrame + toolFrame + toolResultFrame + textFrame + resultFrame
	var wg sync.WaitGroup
	for split := range len(stream) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first, e := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream[:split])})
			if e != nil {
				t.Error(e)
				return
			}
			second, e := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream[first.ConsumedBytes:]), EOF: true})
			if e != nil {
				t.Error(e)
				return
			}
			events, e := replayEvents(append(first.Events, second.Events...))
			if e != nil {
				t.Error(e)
				return
			}
			result, e := a.Complete(execution.CompletionRequest{Events: events})
			if e != nil || result.Kind != execution.TerminalReviewReady {
				t.Error(result, e)
			}
			for _, cause := range []execution.TerminationCause{execution.TerminationDeadlineExceeded, execution.TerminationOutputLimit} {
				result, _ := a.Complete(execution.CompletionRequest{Events: events, Files: execution.TerminalFiles{TerminationCause: cause}})
				if result.Kind != execution.TerminalFailed {
					t.Error("success overrode stop", result)
				}
			}
		}()
	}
	wg.Wait()
	for _, bad := range []string{strings.Repeat("x", maxFrame+1), "\xff\n", `{"type":"text","text":null}` + "\n"} {
		if _, e := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(bad), EOF: true}); e == nil {
			t.Fatal("bad frame accepted")
		}
	}
	d, e := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStderr, Data: []byte("private-error")})
	if e != nil || len(d.Events) != 0 || d.ConsumedBytes != 13 {
		t.Fatal(d, e)
	}
}
