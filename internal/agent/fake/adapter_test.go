package fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestAdapterPrepareAndFingerprintAreDeterministic(t *testing.T) {
	fingerprint := sha256.Sum256([]byte("fake-runtime"))
	adapter := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake", Arguments: []string{"run"}, Environment: map[string]string{"A": "B"}, ResumeMode: execution.ResumeExplicitSession, RuntimeFingerprint: execution.RuntimeFingerprint{Digest: fingerprint, Version: "1.0"}})
	request := execution.StartRequest{WorkspaceRoot: "/workspace", SnapshotDocument: []byte("snapshot"), LaunchToken: execution.LaunchToken{Value: "launch"}}
	first, err := adapter.PrepareStart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.PrepareStart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.LaunchToken() != request.LaunchToken || string(first.Stdin()) != "snapshot" {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	got, err := adapter.Fingerprint(context.Background())
	if err != nil || got.Digest != fingerprint || got.Version != "1.0" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestAdapterPrepareBoundStartPreservesStandardTargetAndFingerprint(t *testing.T) {
	fingerprint := sha256.Sum256([]byte("profile-bound-fake-runtime"))
	adapter := NewAdapter(Plan{
		AdapterID: "pi", Executable: "/fake", Arguments: []string{"run"},
		RuntimeFingerprint: execution.RuntimeFingerprint{Digest: fingerprint, Version: "fixture-v1"},
	})
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := domain.NewContextSnapshotID()
	digest := sha256.Sum256([]byte("bound-snapshot"))
	run, attempt := fakeRunAttemptWithBinding(t, snapshotID, digest, "pi", binding)
	request := execution.StartRequest{
		Run: run, Attempt: attempt, WorkspaceRoot: "/workspace",
		SnapshotDocument: []byte(`{"snapshot":"bound"}`),
		LaunchToken:      execution.LaunchToken{Value: "bound-launch"},
	}
	preparation, err := adapter.PrepareBoundStart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparation.Invocation
	if !preparation.Valid() || invocation.Target().AdapterID() != "pi" ||
		invocation.Target().ProviderID() != domain.DockerExecutionProvider ||
		invocation.LaunchToken() != request.LaunchToken ||
		string(invocation.Stdin()) != string(request.SnapshotDocument) ||
		preparation.RuntimeFingerprint != adapter.plan.RuntimeFingerprint {
		t.Fatalf("preparation=%#v", preparation)
	}
}

func TestPrepareHasNoFilesystemOrProcessSideEffects(t *testing.T) {
	root := t.TempDir()
	adapter := NewAdapter(Plan{AdapterID: "fake", Executable: "/definitely/not/executed"})
	if _, err := adapter.PrepareStart(context.Background(), execution.StartRequest{WorkspaceRoot: root, LaunchToken: execution.LaunchToken{Value: "launch"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("prepare created files: %v", entries)
	}
}

func TestDecodeEventConsumesOnlyCompleteRecordsUntilEOF(t *testing.T) {
	adapter := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake"})
	data := []byte("{\"type\":\"thread.started\",\"thread_id\":\"session-1\"}\n{\"type\":\"turn")
	first, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Offset: 0, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConsumedBytes != bytes.IndexByte(data, '\n')+1 || len(first.Events) != 1 || first.Events[0].ExternalSession != "session-1" {
		t.Fatalf("decoded=%#v", first)
	}
	replay, _ := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake"}).DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Offset: 0, Data: data})
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("non deterministic: %#v %#v", first, replay)
	}
	eof, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Offset: int64(first.ConsumedBytes), Data: data[first.ConsumedBytes:], EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	if eof.ConsumedBytes != len(data)-first.ConsumedBytes || len(eof.Events) != 1 || eof.Events[0].Type != "adapter.parse_error" {
		t.Fatalf("eof=%#v", eof)
	}
}

func TestDecodeEventReplaysEveryByteSplitWithoutLossOrDuplicationOnBothStreams(t *testing.T) {
	data := []byte("{\"type\":\"thread.started\",\"thread_id\":\"session-1\"}\n{\"type\":\"turn.started\"}\n{\"type\":\"turn.completed\"}\n")
	wantTypes := []string{"thread.started", "turn.started", "turn.completed"}
	for _, stream := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		for split := 0; split <= len(data); split++ {
			t.Run(string(stream)+"/split-"+strconv.Itoa(split), func(t *testing.T) {
				first, err := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake"}).DecodeEvent(execution.EventChunk{Stream: stream, Offset: 0, Data: data[:split]})
				if err != nil {
					t.Fatal(err)
				}
				lastNewline := bytes.LastIndexByte(data[:split], '\n')
				wantConsumed := 0
				if lastNewline >= 0 {
					wantConsumed = lastNewline + 1
				}
				if first.ConsumedBytes != wantConsumed {
					t.Fatalf("first consumed=%d want=%d", first.ConsumedBytes, wantConsumed)
				}
				replayed := append(append([]byte(nil), data[first.ConsumedBytes:split]...), data[split:]...)
				second, err := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake"}).DecodeEvent(execution.EventChunk{Stream: stream, Offset: int64(first.ConsumedBytes), Data: replayed, EOF: true})
				if err != nil {
					t.Fatal(err)
				}
				if first.ConsumedBytes+second.ConsumedBytes != len(data) {
					t.Fatalf("final offset=%d want=%d", first.ConsumedBytes+second.ConsumedBytes, len(data))
				}
				events := append(append([]execution.NormalizedEvent(nil), first.Events...), second.Events...)
				if len(events) != len(wantTypes) {
					t.Fatalf("events=%#v", events)
				}
				for index, want := range wantTypes {
					if events[index].Type != want {
						t.Fatalf("events=%#v", events)
					}
				}
			})
		}
	}
}

func TestDecodeTerminalMapsMalformedContractToBusinessFailure(t *testing.T) {
	adapter := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake", TerminalResult: []byte(`{"schema_version":"future"}`)})
	result, err := adapter.DecodeTerminal(execution.TerminalFiles{})
	if err != nil || result.Kind != execution.TerminalFailed || result.FailureReason != "result_contract_invalid" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSnapshotEvidenceConsumesFrozenCandidateAndReportsExclusions(t *testing.T) {
	snapshotID := domain.NewContextSnapshotID()
	candidateID := domain.NewContextRevisionID().String()
	excludedID := domain.NewContextRevisionID().String()
	digest := sha256.Sum256([]byte("snapshot"))
	run, attempt := fakeRunAttempt(t, snapshotID, digest)
	document := []byte(fmt.Sprintf(`{"acceptance_criteria":[{"id":%q}],"decisions":[{"revision_id":%q,"title":"Confirmed title","body":"Confirmed body"}],"selection":{"selected":[{"revision_id":%q,"provenance":{"kind":"accepted_run_candidate"}}],"excluded":[{"revision_id":%q}]}}`, domain.NewCriterionID().String(), candidateID, candidateID, excludedID))
	adapter := NewAdapter(Plan{AdapterID: "fake", Executable: "/fake", SnapshotEvidence: true})
	if _, err := adapter.PrepareStart(context.Background(), execution.StartRequest{Run: run, Attempt: attempt, SnapshotDocument: document, WorkspaceRoot: "/workspace", LaunchToken: execution.LaunchToken{Value: "launch"}}); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DecodeTerminal(execution.TerminalFiles{})
	if err != nil || result.ContextConsumption == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	evidence := result.ContextConsumption
	if evidence.SnapshotID != snapshotID.String() || evidence.SnapshotDigest != fmt.Sprintf("%x", digest) || evidence.CandidateRevisionID != candidateID || evidence.ExcludedRevisionIDs[0] != excludedID || evidence.ExcludedContentObserved || evidence.Derivation != execution.DeriveConfirmedContext(candidateID, "Confirmed title", "Confirmed body") {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func fakeRunAttempt(t *testing.T, snapshotID domain.ContextSnapshotID, digest [32]byte) (domain.AgentRun, domain.Attempt) {
	return fakeRunAttemptWithBinding(t, snapshotID, digest, "fake", domain.AgentExecutionProfileBinding{})
}

func fakeRunAttemptWithBinding(t *testing.T, snapshotID domain.ContextSnapshotID, digest [32]byte, adapterID string, binding domain.AgentExecutionProfileBinding) (domain.AgentRun, domain.Attempt) {
	t.Helper()
	now := time.Now().UTC()
	run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: snapshotID, ContextDigest: digest, AdapterID: adapterID, AgentExecutionProfileBinding: binding, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return run, attempt
}
