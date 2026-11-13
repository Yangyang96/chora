package hermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestCriticalEnvelopeDuplicates(t *testing.T) {
	fields := []struct {
		key, first, second string
	}{
		{"type", `"permission_request"`, `"result"`},
		{"subtype", `"other"`, `"init"`},
		{"text", `"other"`, `"bounded result"`},
		{"name", `"other_tool"`, `"read_file"`},
		{"error", `"private-failure"`, `""`},
		{"session_id", `"other-session"`, `"session-1"`},
		{"tool_call_id", `"other-call"`, `"call-1"`},
		{"exit_code", `7`, `0`},
		{"is_error", `true`, `false`},
		{"replay_gap", `true`, `false`},
	}
	a := &Adapter{}
	for _, field := range fields {
		// Keep each field valid in its own event so rejection tests the duplicate.
		frame := resultFrame
		switch field.key {
		case "subtype":
			frame = initFrame
		case "name", "tool_call_id", "is_error":
			frame = toolResultFrame
		}
		var base map[string]json.RawMessage
		if err := json.Unmarshal([]byte(frame), &base); err != nil {
			t.Fatal(err)
		}
		delete(base, field.key)
		prefix, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		aliases := []string{field.key, strings.ToUpper(field.key), field.key[:1] + strings.ToUpper(field.key[1:])}
		// encoding/json also folds the Unicode long s to ASCII s.
		if strings.Contains(field.key, "s") {
			aliases = append(aliases, strings.ReplaceAll(field.key, "s", "ſ"))
		}
		for aliasIndex, alias := range aliases {
			for _, escaped := range []bool{false, true} {
				key, _ := json.Marshal(alias)
				if escaped {
					key = []byte(`"`)
					for _, char := range alias {
						key = fmt.Appendf(key, `\u%04x`, char)
					}
					key = append(key, '"')
				}
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/alias%d/escaped%t/reverse%t", field.key, aliasIndex, escaped, reverse), func(t *testing.T) {
						first := fmt.Sprintf(`%q:%s`, field.key, field.first)
						second := fmt.Sprintf(`%s:%s`, key, field.second)
						if reverse {
							first, second = second, first
						}
						raw := string(prefix[:len(prefix)-1]) + "," + first + "," + second + "}\n"
						if _, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(raw), EOF: true}); !errors.Is(err, execution.ErrEventStreamInvalid) {
							t.Fatalf("ambiguous %s accepted: %v", field.key, err)
						}
					})
				}
			}
		}
	}
}

func TestEnvelopeAliasesAndNestedMetadata(t *testing.T) {
	a := &Adapter{}
	for _, frame := range []string{initFrame, toolFrame, toolResultFrame, textFrame, resultFrame} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(frame), &fields); err != nil {
			t.Fatal(err)
		}
		aliases := map[string]json.RawMessage{}
		for key, value := range fields {
			aliases[strings.ReplaceAll(strings.ToUpper(key), "S", "ſ")] = value
		}
		// Critical-looking keys inside opaque metadata never identify the envelope.
		aliases["future_metadata"] = json.RawMessage(`{"type":"permission_request","Type":"result","error":"private-failure","Error":"","nested":[{"SESSION_ID":"other"}]}`)
		raw, err := json.Marshal(aliases)
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: append(raw, '\n'), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		want, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(frame), EOF: true})
		if err != nil {
			t.Fatal(err)
		}
		actual, _ := json.Marshal(got.Events)
		expected, _ := json.Marshal(want.Events)
		if string(actual) != string(expected) {
			t.Fatalf("projection differs: got %s want %s", actual, expected)
		}
	}
}

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
