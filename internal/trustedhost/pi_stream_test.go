package trustedhost

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiProgressCaptureDropsCumulativeUpdatesAndPreservesTerminal(t *testing.T) {
	var out bytes.Buffer
	w := &piProgressWriter{target: &out}
	frame, _ := json.Marshal(map[string]string{"type": "tool_execution_update", "output": strings.Repeat("x", 32000)})
	frame = append(frame, '\n')
	for i := 0; i < 400; i++ {
		for _, part := range [][]byte{frame[:17], frame[17:]} {
			if _, err := w.Write(part); err != nil {
				t.Fatal(err)
			}
		}
	}
	terminal := []byte("{\"type\":\"tool_execution_end\"}\n{\"type\":\"agent_settled\"}\n")
	if _, err := w.Write(terminal); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), terminal) {
		t.Fatalf("output=%q", out.Bytes())
	}
}

func TestPiProgressCapturePreservesMalformedOversizedAndPartialRecords(t *testing.T) {
	for _, input := range []string{
		`{"type":"tool_execution_update","broken":` + "\n",
		`{"type":"tool_execution_update","partial":`,
		`{"type":"message_update","broken":` + "\n",
		`{"type":"message_update","partial":`,
		`{"type":"error","text":"tool_execution_update"}` + "\n",
		strings.Repeat("x", piProgressRecordLimit+1) + "\n{\"type\":\"agent_settled\"}\n",
	} {
		var out bytes.Buffer
		w := &piProgressWriter{target: &out}
		for start := 0; start < len(input); {
			end := min(start+77, len(input))
			if _, err := w.Write([]byte(input[start:end])); err != nil {
				t.Fatal(err)
			}
			start = end
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != input {
			t.Fatal("non-transient bytes changed")
		}
	}
}

func TestOSPiCaptureFlushesBeforeWaitAndOwnsDescriptor(t *testing.T) {
	root := t.TempDir()
	stdout, err := os.Create(filepath.Join(root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Create(filepath.Join(root, "progress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := json.Marshal(map[string]string{"type": "tool_execution_update", "output": strings.Repeat("x", 32000)})
	frame = append(frame, '\n')
	modelFrame := []byte("{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"progress\"}}\n")
	for i := 0; i < 400; i++ {
		if _, err := input.Write(modelFrame); err != nil {
			t.Fatal(err)
		}
		if _, err := input.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := InspectExecutable("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	child, err := newOSPlatform().Start(processSpec{
		ExecutableIdentity: executable, Arguments: []string{"-c", `cat progress.jsonl; printf '%s\n' '{"type":"agent_settled"}'; printf partial`},
		Directory: root, Environment: []string{"PATH=/usr/bin:/bin"}, Stdout: stdout, Stderr: stderr, FilterPiProgress: true,
		GateReceiptPath: filepath.Join(root, "receipt"), GateNonce: "capture", GateWait: time.Second,
	})
	stdout.Close()
	stderr.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Release(); err != nil {
		t.Fatal(err)
	}
	if code, err := child.Wait(); err != nil || code != 0 {
		t.Fatalf("exit=%d err=%v", code, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"type\":\"agent_settled\"}\npartial" {
		t.Fatalf("capture=%q", got)
	}
}

func TestPiMessageStreamingDoesNotExhaustPersistedOutputBudget(t *testing.T) {
	var out bytes.Buffer
	w := &piProgressWriter{target: &out}
	// Pi RPC serializes usage on each delta. Ordinary token streaming can
	// exceed the persisted log budget while the final message remains small.
	frame, _ := json.Marshal(map[string]any{"type": "message_update", "usage": map[string]int{"input": 40000, "output": 37000, "cacheRead": 2000000}, "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": strings.Repeat("x", 128)}})
	frame = append(frame, '\n')
	total := 0
	for i := 0; i < 50000; i++ {
		for _, part := range [][]byte{frame[:23], frame[23:]} {
			if _, err := w.Write(part); err != nil {
				t.Fatal(err)
			}
		}
		total += len(frame)
	}
	if int64(total) <= defaultStreamCapacity {
		t.Fatal("fixture did not exceed raw stream budget")
	}
	retained := []byte("{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n{\"type\":\"error\"}\n{\"type\":\"agent_settled\"}\n")
	if _, err := w.Write(retained); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), retained) {
		t.Fatal("final messages or errors changed")
	}
}
