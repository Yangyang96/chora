package codexexec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

// Shapes and pairing follow the pinned 0.155.1 JSONL mapper, including
// command/file-change IDs and completion-only assistant/reasoning items.
func TestToolLifecycleSettlement(t *testing.T) {
	for _, tool := range []string{"command_execution", "file_change"} {
		t.Run(tool, func(t *testing.T) {
			item := func(phase, id, kind string) string {
				return fmt.Sprintf(`{"type":"item.%s","item":{"id":%q,"type":%q}}`+"\n", phase, id, kind)
			}
			otherTool := "file_change"
			if tool == otherTool {
				otherTool = "command_execution"
			}
			start, result := item("started", "tool-1", tool), item("completed", "tool-1", tool)
			cases := []struct{ name, events, reason string }{
				{"paired", start + result, ""},
				{"pending", start, "protocol_invalid"},
				{"orphan", result, "protocol_invalid"},
				{"duplicate-result", start + result + result, "protocol_invalid"},
				{"duplicate-start", start + start + result, "protocol_invalid"},
				{"reused-id", start + result + start + result, "protocol_invalid"},
				{"wrong-id", start + item("completed", "other", tool), "protocol_invalid"},
				{"wrong-type", start + item("completed", "tool-1", otherTool), "protocol_invalid"},
				{"interleaved", start + item("started", "tool-2", tool) + result + item("completed", "tool-2", tool), ""},
				{"informational", item("completed", "reason", "reasoning") + item("completed", "warning", "error"), ""},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					stream := strings.Replace(successStream, `{"type":"item.completed"`, tc.events+`{"type":"item.completed"`, 1)
					a := &Adapter{}
					decoded, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true})
					if err != nil {
						t.Fatal(err)
					}
					got, err := a.Complete(execution.CompletionRequest{Events: persisted(t, decoded.Events)})
					want := execution.TerminalReviewReady
					if tc.reason != "" {
						want = execution.TerminalFailed
					}
					if err != nil || got.Kind != want || got.FailureReason != tc.reason {
						t.Fatalf("kind=%s reason=%s err=%v", got.Kind, got.FailureReason, err)
					}
				})
			}
		})
	}
}

func TestToolFailurePrecedence(t *testing.T) {
	// Even if settlement is invalid, explicit native failure, deadlines and
	// nonzero process exits must not be relabelled as protocol failures.
	pending := `{"type":"item.started","item":{"id":"cmd","type":"command_execution"}}` + "\n"
	stream := strings.Replace(successStream, `{"type":"item.completed"`, pending+`{"type":"item.completed"`, 1)
	for _, tc := range []struct {
		name, suffix, want string
		files              execution.TerminalFiles
	}{
		{"exit", "", "process_failure", execution.TerminalFiles{ExitCode: 2}},
		{"native", `{"type":"turn.failed","error":{"message":"model_not_found"}}` + "\n", "model_unavailable", execution.TerminalFiles{ExitCode: 2}},
		{"deadline", "", string(execution.TerminationDeadlineExceeded), execution.TerminalFiles{TerminationCause: execution.TerminationDeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{}
			d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream + tc.suffix), EOF: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events), Files: tc.files})
			if err != nil || got.Kind != execution.TerminalFailed || got.FailureReason != tc.want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
