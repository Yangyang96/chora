package pi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const fakePiBytes = "#!/bin/sh\necho 0.84.2\n"

func TestPathPiPrepareStartEmitsSessionDirInvocation(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "pi")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte(fakePiBytes), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: executable, Version: "0.84.2", ExecutableSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	sessionRoot := filepath.Join(root, "sessions")
	adapter, err := New(Config{
		PathPiSource: source, SessionRoot: sessionRoot,
		ReadSourceFile: func(string) ([]byte, error) { return []byte(fakePiBytes), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.strictManagedModel {
		t.Fatal("PATH Pi adapter must not enforce the frozen managed model")
	}

	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"` + run.TaskID().String() + `"}}`)
	invocation, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
		WorkspaceRoot: filepath.Join(root, "ws"), LaunchToken: execution.LaunchToken{Value: "launch-path"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--mode", "rpc", "--session-dir", filepath.Join(sessionRoot, attempt.ID().String())}
	if !reflect.DeepEqual(invocation.Arguments(), want) {
		t.Fatalf("arguments = %#v want %#v", invocation.Arguments(), want)
	}
	if invocation.Executable() != executable {
		t.Fatalf("executable = %q want %q", invocation.Executable(), executable)
	}
	predecessor := attempt.ID()
	correction := `{"instructions":"Append the human correction marker","review_decision_id":"exact-reviewed-decision"}`
	successor, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2, Predecessor: &predecessor,
		ContextSnapshotID: attempt.ContextSnapshotID(), ContextDigest: attempt.ContextDigest(),
		AdapterID: AdapterID, AgentExecutionProfileBinding: attempt.AgentExecutionProfileBinding(),
		RetryReason: "Human requested a correction", ContextDelta: correction, CreatedAt: attempt.CreatedAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := adapter.PrepareStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: successor, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
		WorkspaceRoot: filepath.Join(root, "ws"), LaunchToken: execution.LaunchToken{Value: "launch-path-retry"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(retry.Stdin()), []byte{'\n'})
	if len(lines) != 2 || !bytes.Contains(lines[0], []byte(`"type":"get_state"`)) {
		t.Fatalf("successor preflight/prompt frames = %q", retry.Stdin())
	}
	var prompt struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(lines[1], &prompt); err != nil || !strings.Contains(prompt.Message, correction) {
		t.Fatalf("successor prompt omitted the persisted human correction: %v", err)
	}
	if adapter.Capabilities().ResumeMode != execution.ResumeExplicitSession {
		t.Fatal("PATH Pi must advertise explicit-session resume")
	}
	sessionID := "123e4567-e89b-12d3-a456-426614174000"
	resumeAttempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2, Predecessor: &predecessor,
		ContextSnapshotID: attempt.ContextSnapshotID(), ContextDigest: attempt.ContextDigest(), AdapterID: AdapterID,
		AgentExecutionProfileBinding: attempt.AgentExecutionProfileBinding(), ContextDelta: correction,
		ExternalSession: sessionID, CreatedAt: attempt.CreatedAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := adapter.FingerprintForBinding(context.Background(), attempt.AgentExecutionProfileBinding())
	if err != nil {
		t.Fatal(err)
	}
	security := sha256.Sum256([]byte("security"))
	resumed, err := adapter.PrepareResume(context.Background(), execution.ResumeRequest{
		Run: run, Attempt: resumeAttempt, Binding: execution.ResumeBinding{
			ExternalSession: sessionID, SessionOwnerAttemptID: attempt.ID(), RuntimeFingerprint: fingerprint,
			WorkingRoot: filepath.Join(root, "ws"), SecurityFingerprint: security,
		}, SecurityFingerprint: security, DeltaInstruction: []byte(correction), LaunchToken: execution.LaunchToken{Value: "launch-resume"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantResume := []string{"--mode", "rpc", "--session-dir", filepath.Join(sessionRoot, attempt.ID().String()), "--session", sessionID}
	resumeLines := bytes.Split(bytes.TrimSpace(resumed.Stdin()), []byte{'\n'})
	var resumePrompt struct {
		Message string `json:"message"`
	}
	resumePromptErr := error(nil)
	if len(resumeLines) != 2 || !bytes.Contains(resumeLines[0], []byte(`"type":"get_state"`)) {
		resumePromptErr = errors.New("missing correlated get_state")
	} else {
		resumePromptErr = json.Unmarshal(resumeLines[1], &resumePrompt)
	}
	if !reflect.DeepEqual(resumed.Arguments(), wantResume) || resumePromptErr != nil || resumePrompt.Message != correction {
		t.Fatalf("resume invocation = %#v stdin=%q", resumed.Arguments(), resumed.Stdin())
	}
}

func TestPathPiGetStateProjectsValidatedSessionIdentity(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	if err := os.WriteFile(executable, []byte(fakePiBytes), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: executable, Version: "0.85.1", ExecutableSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{PathPiSource: source, SessionRoot: filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"id":"chora-get-state","type":"response","command":"get_state","success":true,"data":{"sessionId":"123e4567-e89b-12d3-a456-426614174000","sessionFile":"/private/ignored.jsonl"}}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "agent.session" || decoded.Events[0].ExternalSession != "123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("decoded get_state = %#v, %v", decoded, err)
	}
	_, err = adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(`{"type":"response","command":"get_state","success":true,"data":{"sessionId":"not-a-uuid"}}` + "\n")})
	if err == nil {
		t.Fatal("invalid session identity accepted")
	}
}

func TestPathPiDecodeTerminalSynthesizesReviewReadyWithoutResultFile(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	if err := os.WriteFile(executable, []byte(fakePiBytes), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: executable, Version: "0.84.2", ExecutableSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{PathPiSource: source, SessionRoot: filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{}, ExitCode: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != execution.TerminalReviewReady || result.Summary == "" {
		t.Fatalf("synthesized result = %#v", result)
	}
}

func TestPathPiDecodeRecordsUserModelWithoutRejection(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	if err := os.WriteFile(executable, []byte(fakePiBytes), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: executable, Version: "0.84.2", ExecutableSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{PathPiSource: source, SessionRoot: filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"type":"model_identity","model_id":"deepseek-r1:8b","provider":"ollama","timestamp":"2026-09-03T12:00:00Z"}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line, EOF: true})
	if err != nil {
		t.Fatalf("DecodeEvent rejected user model: %v", err)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Type != "model_identity" {
		t.Fatalf("events = %#v", decoded.Events)
	}
	want := `{"event_type":"model_identity","model_id":"deepseek-r1:8b","provider":"ollama","timestamp":"2026-09-03T12:00:00Z"}`
	if string(decoded.Events[0].NormalizedJSON) != want {
		t.Fatalf("normalized = %q want %q", decoded.Events[0].NormalizedJSON, want)
	}
}

func TestPathPiUsesNativePermissionPolicyAndRedactsAbsoluteHostPaths(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pi")
	if err := os.WriteFile(executable, []byte(fakePiBytes), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: executable, Version: "0.84.2", ExecutableSHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{PathPiSource: source, SessionRoot: filepath.Join(root, "sessions")})
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"type":"tool_execution_start","toolCallId":"native-command","toolName":"bash","args":{"command":"cat /Users/example/private.txt"}}` + "\n")
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: line, EOF: true})
	if err != nil || len(decoded.Events) != 1 || decoded.Events[0].Type != "tool_start" {
		t.Fatalf("native permission projection = %#v, %v", decoded, err)
	}
	visible := decoded.Events[0].NormalizedJSON
	if !bytes.Contains(visible, []byte(`"redacted_command_summary":"cat [host path redacted]"`)) || bytes.Contains(visible, []byte("/Users/")) {
		t.Fatalf("unsafe PATH Pi activity projection = %s", visible)
	}
}
