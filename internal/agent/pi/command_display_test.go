package pi

import (
	"encoding/json"
	"github.com/Yangyang96/chora/internal/execution"
	"strings"
	"testing"
)

func TestNativeCommandDisplayRetainsCommandAndBoundsPaths(t *testing.T) {
	root := "/private/task space"
	for _, tc := range []struct{ in, want string }{
		{`cat /Users/alice/private.txt && npm test`, `cat [host path redacted] && npm test`},
		{`cd "/private/task space/repo-a" && npm test`, `cd "repo-a" && npm test`},
		{`cd /private/task\ space/repo-a && npm test`, `cd 'repo-a' && npm test`},
		{`cat '/private/task space/repo-a/a b.txt'`, `cat 'repo-a/a b.txt'`},
		{`cat "/Users/alice/private folder/a.txt" | wc -l`, `cat "[host path redacted]" | wc -l`},
		{`cat "/private/task space/../private.txt"`, `cat "[host path redacted]"`},
		{`cat "/private/task space-other/a.txt"`, `cat "[host path redacted]"`},
		{`npm test -- --label "plain text" > /dev/null`, `npm test -- --label "plain text" > /dev/null`},
		{`curl https://example.com/a`, `curl https://example.com/a`},
		{`API_TOKEN=abc cat /Users/alice/a`, `[REDACTED]`},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := redactHostCommand(tc.in, root); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestNativeCommandProjectionKeepsOriginalDigestAndManagedRejection(t *testing.T) {
	command := `cd "/private/task space/repo-a" && npm test`
	raw, _ := json.Marshal(map[string]any{"type": "tool_execution_start", "toolName": "bash", "toolCallId": "call-1", "args": map[string]string{"command": command}})
	event, ok, err := projectLine(raw, false, "/private/task space")
	if err != nil || !ok {
		t.Fatal(err)
	}
	var p map[string]any
	if err = json.Unmarshal(event.NormalizedJSON, &p); err != nil {
		t.Fatal(err)
	}
	if p["redacted_command_summary"] != `cd "repo-a" && npm test` || p["command_digest"] != digest([]byte(command)) {
		t.Fatalf("projection: %s", event.NormalizedJSON)
	}
	other, _, _ := projectLine(raw, false, "/private/another-task")
	if strings.Contains(string(other.NormalizedJSON), "task space") {
		t.Fatalf("cross-task path exposed: %s", other.NormalizedJSON)
	}
	managed, _, err := projectLine(raw, true, "/private/task space")
	if err != nil || managed.Type != "error" || !strings.Contains(string(managed.NormalizedJSON), "undeclared_path") {
		t.Fatalf("managed policy weakened: %s %v", managed.NormalizedJSON, err)
	}
}

func TestNativeDecodeUsesPerChunkTrustedWorkingRoot(t *testing.T) {
	adapter := &Adapter{}
	data := []byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"cd /private/task-a/repo-a && npm test"}}` + "\n")
	for _, root := range []string{"/private/task-a", "/private/task-b"} {
		result, err := adapter.DecodeEvent(execution.EventChunk{WorkingRoot: root, Stream: execution.StreamStdout, Data: data, EOF: true})
		if err != nil || len(result.Events) != 1 {
			t.Fatalf("decode: %v %v", result, err)
		}
		text := string(result.Events[0].NormalizedJSON)
		if strings.Contains(text, "/private/") || (root == "/private/task-a" && !strings.Contains(text, "cd 'repo-a'")) || (root == "/private/task-b" && !strings.Contains(text, "[host path redacted]")) {
			t.Fatal(text)
		}
	}
}
