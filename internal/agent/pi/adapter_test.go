package pi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/processbound"
)

func TestPrepareStartEmitsFrozenInvocation(t *testing.T) {
	adapter := testAdapter(t, nil)
	run, attempt, snapshot := testRunAttempt(t)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"` + run.TaskID().String() + `"}}`)
	root := filepath.Join(t.TempDir(), "source")
	invocation, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: root,
		LaunchToken: execution.LaunchToken{Value: "launch-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.AdapterID() != AdapterID || invocation.Executable() != Executable || invocation.WorkingRoot() != root {
		t.Fatalf("identity = %q %q %q", invocation.AdapterID(), invocation.Executable(), invocation.WorkingRoot())
	}
	if !reflect.DeepEqual(invocation.Arguments(), exactRPCArguments) {
		t.Fatalf("arguments = %#v", invocation.Arguments())
	}
	standardBundle, err := ManagedCapabilityBundleForProfile(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	wantEnvironment := map[string]string{
		"CHORA_RUN_ID": run.ID().String(), "CHORA_ATTEMPT_ID": attempt.ID().String(),
		"CHORA_CONTEXT_DIGEST": digestHex(snapshot), "CHORA_POLICY_DIGEST": PolicySHA256,
		"CHORA_CONTRACT_DIGEST":  digestHex(contract),
		"CHORA_ATTEMPT_IMAGE_ID": AttemptImageID, "CHORA_BOUNDARY_IMAGE_ID": BoundaryImageID,
		"CHORA_AGENT_EXECUTION_PROFILE":  string(domain.AgentExecutionProfileStandard),
		"CHORA_RUNTIME_SOURCE":           domain.ManagedPiRuntimeSource,
		"CHORA_RUNTIME_SOURCE_IDENTITY":  hex.EncodeToString(adapter.managedSource.sourceIdentity[:]),
		"CHORA_RUNTIME_VERSION":          RuntimeVersion,
		"CHORA_EXECUTION_PROVIDER":       domain.DockerExecutionProvider,
		"CHORA_CAPABILITY_POLICY":        domain.StandardCapabilityPolicy,
		"CHORA_CAPABILITY_BUNDLE_ID":     standardBundle.ID(),
		"CHORA_CAPABILITY_BUNDLE_SHA256": standardBundle.SHA256(),
		"CHORA_TRUST_DISCLOSURE_POLICY":  "",
		"CHORA_COMMAND_TIMEOUT_SECONDS":  "600", "CHORA_ATTEMPT_TIMEOUT_SECONDS": "1200",
		"CHORA_PERSISTED_LOG_LIMIT_BYTES": "10485760", "CHORA_ARTIFACT_LIMIT_BYTES": "104857600",
		"CHORA_RESULT_PATH": ResultPath, "CHORA_PATCH_PATH": PatchPath,
		"CHORA_CHECKS_PATH": ChecksPath, "CHORA_RUNTIME_IDENTITY_PATH": RuntimeIdentityPath,
	}
	if !reflect.DeepEqual(invocation.Environment(), wantEnvironment) {
		t.Fatalf("environment = %#v", invocation.Environment())
	}
	var command struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	stdin := invocation.Stdin()
	if len(stdin) == 0 || stdin[len(stdin)-1] != '\n' || strings.Count(string(stdin), "\n") != 1 || json.Unmarshal(stdin[:len(stdin)-1], &command) != nil {
		t.Fatalf("stdin is not one strict JSONL record: %q", stdin)
	}
	gotSnapshot, gotContract, ok := execution.UnwrapFrozenExecutionPrompt(command.Message)
	if command.ID != "chora-prompt" || command.Type != "prompt" || !ok || !bytes.Equal(gotSnapshot, snapshot) || !bytes.Equal(gotContract, contract) || !strings.Contains(command.Message, "Do not add flags or prepend, append, redirect, or combine shell commands") {
		t.Fatalf("prompt = %+v", command)
	}
}

func TestManagedProfilesBindDistinctExactCapabilityBundles(t *testing.T) {
	adapter := testAdapter(t, nil)
	contractFor := func(run domain.AgentRun) []byte {
		return []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"` + run.TaskID().String() + `"}}`)
	}
	type observation struct {
		bundle      ManagedCapabilityBundle
		fingerprint execution.RuntimeFingerprint
	}
	observed := map[domain.AgentExecutionProfile]observation{}
	for _, profile := range []domain.AgentExecutionProfile{
		domain.AgentExecutionProfileMinimal,
		domain.AgentExecutionProfileStandard,
	} {
		run, attempt, snapshot := testRunAttemptForProfile(t, profile)
		preparation, err := adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
			Run: run, Attempt: attempt, SnapshotDocument: snapshot,
			ExecutionContractDocument: contractFor(run), WorkspaceRoot: filepath.Join(t.TempDir(), "source"),
			LaunchToken: execution.LaunchToken{Value: "launch-" + string(profile)},
		})
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := ManagedCapabilityBundleForProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		environment := preparation.Invocation.Environment()
		if environment["CHORA_CAPABILITY_BUNDLE_ID"] != bundle.ID() ||
			environment["CHORA_CAPABILITY_BUNDLE_SHA256"] != bundle.SHA256() {
			t.Fatalf("%s bundle environment = %#v", profile, environment)
		}
		observed[profile] = observation{bundle: bundle, fingerprint: preparation.RuntimeFingerprint}
	}

	minimal := observed[domain.AgentExecutionProfileMinimal]
	standard := observed[domain.AgentExecutionProfileStandard]
	if !reflect.DeepEqual(minimal.bundle.Tools(), []string{"read", "bash", "edit", "write"}) {
		t.Fatalf("Minimal tools = %#v", minimal.bundle.Tools())
	}
	if !reflect.DeepEqual(standard.bundle.Tools(), []string{"read", "bash", "edit", "write", "grep", "find", "ls"}) {
		t.Fatalf("Standard tools = %#v", standard.bundle.Tools())
	}
	if minimal.bundle.ID() == standard.bundle.ID() || minimal.bundle.SHA256() == standard.bundle.SHA256() {
		t.Fatalf("managed profile capability identity collapsed: minimal=%#v standard=%#v", minimal, standard)
	}
	if minimal.fingerprint != standard.fingerprint {
		t.Fatalf("managed capability policy changed source-only Runtime fingerprint: minimal=%#v standard=%#v", minimal, standard)
	}
	tools := minimal.bundle.Tools()
	tools = append(tools, "grep")
	if reflect.DeepEqual(tools, minimal.bundle.Tools()) {
		t.Fatal("ManagedCapabilityBundle.Tools returned mutable internal state")
	}
	if _, err := ManagedCapabilityBundleForProfile(domain.AgentExecutionProfileTrustedLocal); err == nil {
		t.Fatal("Trusted Local received a managed capability bundle")
	}
}

func TestPrepareStartRejectsInvalidInputs(t *testing.T) {
	adapter := testAdapter(t, nil)
	run, attempt, snapshot := testRunAttempt(t)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"` + run.TaskID().String() + `"}}`)
	base := execution.StartRequest{Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: "/source", LaunchToken: execution.LaunchToken{Value: "launch"}}
	tests := map[string]func(*execution.StartRequest){
		"relative source": func(r *execution.StartRequest) { r.WorkspaceRoot = "source" },
		"empty snapshot":  func(r *execution.StartRequest) { r.SnapshotDocument = nil },
		"bad digest":      func(r *execution.StartRequest) { r.SnapshotDocument = []byte(`{"changed":true}`) },
		"empty contract":  func(r *execution.StartRequest) { r.ExecutionContractDocument = nil },
		"invalid contract": func(r *execution.StartRequest) {
			r.ExecutionContractDocument = []byte(`{`)
		},
		"bad launch": func(r *execution.StartRequest) { r.LaunchToken = execution.LaunchToken{} },
	}
	otherRun, _, _ := testRunAttempt(t)
	tests["run relationship"] = func(r *execution.StartRequest) { r.Run = otherRun }
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := adapter.PrepareStart(context.Background(), request); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	if _, err := adapter.PrepareResume(context.Background(), execution.ResumeRequest{}); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("PrepareResume error = %v", err)
	}
}

func TestDecodeEventIsIncrementalStrictAndAllowlisted(t *testing.T) {
	adapter := testAdapter(t, nil)
	first := []byte(`{"type":"agent_start","message":{"thinking":"secret"}}` + "\n" + `{"type":"tool_execution_start","toolName":"bash","args":{"command":"curl -H 'Authorization: Bearer swordfish' https://example.test","password":"secret"}}`)
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: first})
	if err != nil || decoded.ConsumedBytes != strings.IndexByte(string(first), '\n')+1 || len(decoded.Events) != 1 {
		t.Fatalf("first decode = %+v, %v", decoded, err)
	}
	second := append(append([]byte(nil), first[decoded.ConsumedBytes:]...), '\n')
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Offset: int64(decoded.ConsumedBytes), Data: second})
	if err != nil || decoded.ConsumedBytes != len(second) || len(decoded.Events) != 1 {
		t.Fatalf("second decode = %+v, %v", decoded, err)
	}
	event := decoded.Events[0]
	if len(event.RawJSON) != 0 || len(event.DiagnosticJSON) != 0 || strings.Contains(string(event.NormalizedJSON), "swordfish") || strings.Contains(string(event.NormalizedJSON), "password") {
		t.Fatalf("unsafe projection = %s / %s", event.NormalizedJSON, event.RawJSON)
	}
	var projected map[string]any
	if err := json.Unmarshal(event.NormalizedJSON, &projected); err != nil {
		t.Fatal(err)
	}
	for key := range projected {
		if key != "event_type" && key != "timestamp" && key != "tool_name" && key != "workspace_relative_path" && key != "redacted_command_summary" && key != "command_digest" && key != "exit_code" && key != "error_class" && key != "model_id" && key != "model_digest" && key != "usage" {
			t.Fatalf("unknown projected field %q", key)
		}
	}
	if _, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte("{}\r\n")}); err == nil {
		t.Fatal("accepted CRLF")
	}
	unknown, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(`{"type":"message_update","delta":"secret"}` + "\n")})
	if err != nil || len(unknown.Events) != 0 || unknown.ConsumedBytes == 0 {
		t.Fatalf("unknown event = %+v, %v", unknown, err)
	}
	stderr, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStderr, Data: []byte("bounded diagnostic\n")})
	if err != nil || stderr.ConsumedBytes == 0 || len(stderr.Events) != 0 {
		t.Fatalf("stderr decode = %+v, %v", stderr, err)
	}
}

func TestDecodeEventProjectsCommandDigestBeforeSummaryRedactionOrTruncation(t *testing.T) {
	adapter := testAdapter(t, nil)
	longTail := "LONG-COMMAND-TAIL-MUST-NOT-PERSIST"
	longCommand := "custom-check " + strings.Repeat("x", maxCommandSummary) + longTail
	tests := []struct {
		name        string
		command     string
		wantSummary string
		mustHide    string
	}{
		{name: "sensitive word", command: "custom-check token-marker", wantSummary: "[REDACTED]", mustHide: "token-marker"},
		{name: "long argv", command: longCommand, wantSummary: longCommand[:maxCommandSummary], mustHide: longTail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, err := json.Marshal(map[string]any{
				"type":       "tool_execution_start",
				"toolCallId": "check-call",
				"toolName":   "bash",
				"args":       map[string]string{"command": test.command},
			})
			if err != nil {
				t.Fatal(err)
			}
			line = append(line, '\n')
			decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line, EOF: true})
			if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_start" {
				t.Fatalf("DecodeEvent() = %#v, %v", decoded, err)
			}
			var projected struct {
				Summary string `json:"redacted_command_summary"`
				Digest  string `json:"command_digest"`
			}
			if err := json.Unmarshal(decoded.Events[0].NormalizedJSON, &projected); err != nil {
				t.Fatal(err)
			}
			if projected.Summary != test.wantSummary || projected.Digest != digest([]byte(test.command)) {
				t.Fatalf("command projection summary=%q digest=%q", projected.Summary, projected.Digest)
			}
			if bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(test.mustHide)) || len(decoded.Events[0].RawJSON) != 0 || len(decoded.Events[0].DiagnosticJSON) != 0 {
				t.Fatal("normalized command activity retained unprojected command text")
			}
		})
	}
}

func TestDecodeEventProjectsOnlyCompletedAssistantText(t *testing.T) {
	adapter := testAdapter(t, nil)
	line := []byte(`{"type":"message_end","timestamp":"2026-08-25T01:02:03Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden chain"},{"type":"text","text":"完成第一部分。"},{"type":"toolCall","name":"bash","arguments":{"token":"secret"}},{"type":"text","text":"完成第二部分。"}]}}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line, EOF: true})
	if err != nil || len(decoded.Events) != 1 {
		t.Fatalf("assistant message decode = %#v, %v", decoded, err)
	}
	event := decoded.Events[0]
	if event.Type != "assistant_message" || len(event.RawJSON) != 0 || len(event.DiagnosticJSON) != 0 {
		t.Fatalf("assistant message event = %#v", event)
	}
	visible := string(event.NormalizedJSON)
	if !strings.Contains(visible, `"text":"完成第一部分。\n\n完成第二部分。"`) || strings.Contains(visible, "hidden chain") || strings.Contains(visible, "toolCall") || strings.Contains(visible, "secret") {
		t.Fatalf("unsafe assistant projection = %s", visible)
	}

	for _, ignored := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"partial"}}`,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"raw prompt"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"}]}}`,
	} {
		decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(ignored + "\n"), EOF: true})
		if err != nil || len(decoded.Events) != 0 {
			t.Fatalf("unsafe event was projected: %s => %#v, %v", ignored, decoded, err)
		}
	}
}

func TestDecodeEventProjectsRealPiRPCFrames(t *testing.T) {
	adapter := testAdapter(t, nil)

	settled := []byte(`{"type":"agent_settled"}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: settled, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "agent_settled" {
		t.Fatalf("agent_settled decode = %#v, %v", decoded, err)
	}

	success := []byte(`{"id":"chora-prompt","type":"response","command":"prompt","success":true}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: success, EOF: true})
	if err != nil || len(decoded.Events) != 0 {
		t.Fatalf("response success should be dropped = %#v, %v", decoded, err)
	}

	failure := []byte(`{"id":"chora-prompt","type":"response","command":"prompt","success":false,"error":"HTTP 402 subscription required"}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: failure, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "error" ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"error_class":"runtime_error"`)) {
		t.Fatalf("response failure decode = %#v, %v", decoded, err)
	}

	message := []byte(`{"type":"message_end","timestamp":"2026-08-25T01:02:03Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"provider":"openai-codex","model":"gpt-5.6-sol","usage":{"input":10,"output":4,"totalTokens":14}}}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: message, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "assistant_message" {
		t.Fatalf("assistant message decode = %#v, %v", decoded, err)
	}
	if !bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"provider":"openai-codex"`)) ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"model_id":"gpt-5.6-sol"`)) ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}`)) {
		t.Fatalf("assistant provenance = %s", decoded.Events[0].NormalizedJSON)
	}

	tool := []byte(`{"type":"tool_execution_end","toolCallId":"call-17","toolName":"bash","result":{"details":{"exitCode":3},"content":"out"},"isError":true}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: tool, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_end" {
		t.Fatalf("tool_end decode = %#v, %v", decoded, err)
	}
	if !bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"exit_code":3`)) ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"tool_error":true`)) ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"tool_call_id":"call-17"`)) {
		t.Fatalf("tool provenance = %s", decoded.Events[0].NormalizedJSON)
	}

	start := []byte(`{"type":"tool_execution_start","toolCallId":"call-17","toolName":"bash","args":{"command":"go test ./internal/app"}}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: start, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_start" ||
		!bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"tool_call_id":"call-17"`)) {
		t.Fatalf("tool_start correlation identity = %#v, %v", decoded, err)
	}
}

func TestDecodeEventProjectsAssistantModelErrorWithoutVendorDiagnostic(t *testing.T) {
	adapter := &Adapter{}
	line := []byte(`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"ollama","model":"paid-model","stopReason":"error","errorMessage":"402 account URL and private diagnostic"}}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "error" {
		t.Fatalf("assistant model error decode = %#v, %v", decoded, err)
	}
	visible := string(decoded.Events[0].NormalizedJSON)
	if !strings.Contains(visible, `"error_class":"model_error"`) || !strings.Contains(visible, `"provider":"ollama"`) || !strings.Contains(visible, `"model_id":"paid-model"`) || strings.Contains(visible, "402") || strings.Contains(visible, "private diagnostic") || len(decoded.Events[0].RawJSON) != 0 || len(decoded.Events[0].DiagnosticJSON) != 0 {
		t.Fatalf("unsafe or incomplete model error projection = %s", visible)
	}
}

func TestDecodeEventBoundsAssistantTextAtUTF8Boundary(t *testing.T) {
	adapter := testAdapter(t, nil)
	message := map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": strings.Repeat("界", maxAssistantText)}}}}
	encoded, _ := json.Marshal(message)
	encoded = append(encoded, '\n')
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: encoded, EOF: true})
	if err != nil || len(decoded.Events) != 1 || !bytes.Contains(decoded.Events[0].NormalizedJSON, []byte(`"truncated":true`)) || !json.Valid(decoded.Events[0].NormalizedJSON) {
		t.Fatalf("bounded assistant message = %#v, %v", decoded, err)
	}
}

func TestDecodeEventProjectsUndeclaredPathAsPolicyViolation(t *testing.T) {
	adapter := testAdapter(t, nil)
	violating := []byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"cat /Users/example/private.txt"}}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: violating, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "error" || !strings.Contains(string(decoded.Events[0].NormalizedJSON), `"error_class":"undeclared_path"`) || strings.Contains(string(decoded.Events[0].NormalizedJSON), "/Users/") {
		t.Fatalf("policy projection = %#v, %v", decoded, err)
	}
	allowed := []byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"cat /workspace/repository/probe.txt"}}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: allowed, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_start" {
		t.Fatalf("declared path projection = %#v, %v", decoded, err)
	}
	url := []byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"curl https://example.test/path"}}` + "\n")
	decoded, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: url, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_start" {
		t.Fatalf("URL was mistaken for a host path = %#v, %v", decoded, err)
	}
}

func TestDecodeEventSanitizesRuntimeControlledErrorClass(t *testing.T) {
	adapter := testAdapter(t, nil)
	unsafe := []byte(`{"type":"error","name":"swordfish"}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: unsafe, EOF: true})
	if err != nil || len(decoded.Events) != 1 {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	visible := string(decoded.Events[0].NormalizedJSON)
	if !strings.Contains(visible, `"error_class":"runtime_error"`) || strings.Contains(visible, "alice") || strings.Contains(visible, "swordfish") {
		t.Fatalf("unsafe error projection = %s", visible)
	}
}

func TestDecodeTerminalUsesStrictResultContract(t *testing.T) {
	result := []byte(`{"schema_version":"chora.agent-result.v1","summary":"done","review_ready":true,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":false,"reason":""}}`)
	adapter := testAdapter(t, func(string) ([]byte, error) { return result, nil })
	decoded, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{"result": "/output/result.json"}})
	if err != nil || decoded.Kind != execution.TerminalReviewReady {
		t.Fatalf("DecodeTerminal = %+v, %v", decoded, err)
	}
	if _, err := adapter.DecodeTerminal(execution.TerminalFiles{}); err == nil {
		t.Fatal("accepted missing result")
	}
	adapter = testAdapter(t, func(string) ([]byte, error) { return []byte(`{"unknown":true}`), nil })
	if _, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{"result": "/output/result.json"}}); !errors.Is(err, agent.ErrResultContractInvalid) {
		t.Fatalf("malformed result error = %v", err)
	}
	adapter = testAdapter(t, func(string) ([]byte, error) { return make([]byte, processbound.ResultLimit+1), nil })
	if _, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{"result": "/output/result.json"}}); !errors.Is(err, processbound.ErrOutputLimit) {
		t.Fatalf("oversize result error = %v", err)
	}
}

func TestConformanceFingerprintBindsFrozenIdentities(t *testing.T) {
	adapter := testAdapter(t, nil)
	if err := agent.CheckConformance(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}
	first, err := adapter.Fingerprint(context.Background())
	if err != nil || !first.Valid() || first.Version != RuntimeVersion {
		t.Fatalf("Fingerprint = %+v, %v", first, err)
	}
	changed := testConfig(t, nil)
	changed.AttemptImageID = "sha256:not-a-digest"
	if _, err := New(changed); err == nil {
		t.Fatal("accepted malformed image identity")
	}
	releaseImage := "sha256:" + strings.Repeat("f", 64)
	changed = testConfig(t, nil)
	changed.AttemptImageID = releaseImage
	if adapter, err := New(changed); err != nil || adapter.managedSource.RuntimeImageID() != releaseImage {
		t.Fatalf("release-pinned image identity was not accepted: source=%#v err=%v", adapter, err)
	}
}

func testAdapter(t *testing.T, readFile func(string) ([]byte, error)) *Adapter {
	t.Helper()
	adapter, err := New(testConfig(t, readFile))
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func testConfig(t *testing.T, readFile func(string) ([]byte, error)) Config {
	t.Helper()
	runtimeConfig, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "g2-m1a", "sandbox-policy.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	return Config{RuntimeConfig: runtimeConfig, Policy: policy, AttemptImageID: AttemptImageID, BoundaryImageID: BoundaryImageID, ReadFile: readFile}
}

func testRunAttempt(t *testing.T) (domain.AgentRun, domain.Attempt, []byte) {
	return testRunAttemptForProfile(t, domain.AgentExecutionProfileStandard)
}

func testRunAttemptForProfile(t *testing.T, profile domain.AgentExecutionProfile) (domain.AgentRun, domain.Attempt, []byte) {
	t.Helper()
	now := time.Now().UTC()
	run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"schema_version":"chora.context-snapshot.v1","content":"frozen"}`)
	binding, err := domain.NewAgentExecutionProfileBinding(profile)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1,
		ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256(snapshot),
		AdapterID: AdapterID, AgentExecutionProfileBinding: binding, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run, attempt, snapshot
}

func digestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestReportedPiCostKeepsUnknownDistinctFromExplicitZero(t *testing.T) {
	for _, input := range []string{`{}`, `{"cost":{"total":null}}`, `{"cost":{"total":-1}}`} {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal([]byte(input), &raw)
		if _, ok := reportedPiCost(raw); ok {
			t.Fatalf("invalid/unavailable cost accepted: %s", input)
		}
	}
	for _, input := range []string{`{"cost":{"total":0}}`, `{"cost":{"total":0.125}}`} {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal([]byte(input), &raw)
		if _, ok := reportedPiCost(raw); !ok {
			t.Fatalf("reported cost lost: %s", input)
		}
	}
}

func TestResourceCommandSemanticBindingSurvivesQuotingAndRedaction(t *testing.T) {
	repo := domain.NewRepositoryID().String()
	root := "/private/Task root"
	values := []string{
		"cd '" + repo + "/module space' && custom-check 'token-value'",
		"cd \"" + repo + "/module space\" && custom-check \"token-value\"",
		"cd '" + root + "/" + repo + "/module space' && custom-check 'token-value'",
	}
	var identity string
	for _, command := range values {
		raw, _ := json.Marshal(map[string]any{"type": "tool_execution_start", "toolName": "bash", "toolCallId": "call", "args": map[string]string{"command": command}})
		event, keep, err := projectLine(raw, false, root)
		if err != nil || !keep {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(event.NormalizedJSON, &value); err != nil {
			t.Fatal(err)
		}
		got, _ := value["resource_command_digest"].(string)
		if len(got) != 64 {
			t.Fatal("missing semantic digest")
		}
		if identity != "" && got != identity {
			t.Fatal("equivalent quotes lost attribution")
		}
		identity = got
		if strings.Contains(string(event.NormalizedJSON), "token-value") {
			t.Fatal("sensitive argv leaked")
		}
	}
}

func TestResourceCommandAbsoluteBindingIsTaskScopedAndSupportsShortLocator(t *testing.T) {
	root := "/private/task"
	short := "0123456789ab"
	digestFor := func(command, workingRoot string, strict bool) (map[string]any, execution.NormalizedEvent) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": "tool_execution_start", "toolName": "bash", "toolCallId": "call", "args": map[string]string{"command": command}})
		event, keep, err := projectLine(raw, strict, workingRoot)
		if err != nil || !keep {
			t.Fatalf("projectLine(%q) keep=%v err=%v", command, keep, err)
		}
		var value map[string]any
		if err := json.Unmarshal(event.NormalizedJSON, &value); err != nil {
			t.Fatal(err)
		}
		return value, event
	}

	relative, _ := digestFor("cd "+short+" && npm test", root, false)
	absolute, _ := digestFor("cd "+root+"/"+short+" && npm test", root, false)
	if relative["resource_command_digest"] == nil || relative["resource_command_digest"] != absolute["resource_command_digest"] {
		t.Fatalf("relative=%v absolute=%v", relative, absolute)
	}
	other, _ := digestFor("cd "+root+"/abcdef012345 && npm test", root, false)
	if other["resource_command_digest"] == absolute["resource_command_digest"] {
		t.Fatal("cross-repository command shared semantic identity")
	}
	outside, _ := digestFor("cd /private/task-sibling/"+short+" && npm test", root, false)
	if _, ok := outside["resource_command_digest"]; ok || strings.Contains(outside["redacted_command_summary"].(string), "/private/") {
		t.Fatalf("outside path gained attribution or leaked: %v", outside)
	}
	managed, managedEvent := digestFor("cd "+root+"/"+short+" && npm test", root, true)
	if managedEvent.Type != "error" || managed["error_class"] != "undeclared_path" {
		t.Fatalf("managed strict path accepted: %v", managed)
	}
	if _, ok := managed["resource_command_digest"]; ok {
		t.Fatal("managed path rejection retained command attribution")
	}
}
