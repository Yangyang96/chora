package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

type resourceObserverAdapterFixture struct {
	adapter    *Adapter
	binding    ResourceObserverBinding
	root       string
	helperPath string
}

func newResourceObserverAdapterFixture(t *testing.T) resourceObserverAdapterFixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(root, "pi")
	if err := os.WriteFile(piPath, []byte(fakePiBytes), 0o700); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(root, "chora")
	if err := os.WriteFile(helperPath, []byte("bounded fingerprint helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	binding, err := InstallResourceObserver(filepath.Join(root, "observer"), helperPath)
	if err != nil {
		t.Fatal(err)
	}
	piSum := sha256.Sum256([]byte(fakePiBytes))
	source, err := NewPathPiSource(PathPiSourceParams{
		ExecutablePath: piPath, Version: "0.85.1", ExecutableSHA256: piSum,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{
		PathPiSource: source, SessionRoot: filepath.Join(root, "sessions"), ResourceObserver: &binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resourceObserverAdapterFixture{adapter: adapter, binding: binding, root: root, helperPath: helperPath}
}

func resourceObserverV12Contract(t *testing.T, run domain.AgentRun) []byte {
	t.Helper()
	repositoryID := domain.NewRepositoryID().String()
	contract, err := json.Marshal(map[string]any{
		"schema_version": speccoding.CoreContractSchemaVersionV12,
		"task": map[string]any{
			"id": run.TaskID().String(),
			"resources": []map[string]any{{
				"repo_id": repositoryID, "name": "repository", "locator": repositoryID,
				"target_identity_digest": strings.Repeat("1", 64), "role": "write",
				"base_commit": strings.Repeat("2", 40), "base_tree": strings.Repeat("3", 40), "base_ref": "HEAD",
				"scope":  map[string]any{"mode": "repository", "migrationChoice": "not_needed"},
				"checks": map[string]any{"mode": "none", "commands": []any{}, "preparation": []any{}, "selectionSource": "user"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func TestResourceObserverV12StartBindsExtensionConfigurationAndFingerprint(t *testing.T) {
	fixture := newResourceObserverAdapterFixture(t)
	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	contract := resourceObserverV12Contract(t, run)
	root := filepath.Join(fixture.root, "task-root")
	preparation, err := fixture.adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
		WorkspaceRoot: root, LaunchToken: execution.LaunchToken{Value: "observer-v12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArguments := []string{"--mode", "rpc", "--extension", fixture.binding.ExtensionPath, "--session-dir", filepath.Join(fixture.root, "sessions", attempt.ID().String())}
	if !reflect.DeepEqual(preparation.Invocation.Arguments(), wantArguments) {
		t.Fatalf("arguments = %#v want %#v", preparation.Invocation.Arguments(), wantArguments)
	}
	environment := preparation.Invocation.Environment()
	configPath := environment["CHORA_CHECK_OBSERVER_CONFIG"]
	configBody, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configSum := sha256.Sum256(configBody)
	if environment["CHORA_CHECK_OBSERVER_CONFIG_SHA256"] != hex.EncodeToString(configSum[:]) ||
		environment["CHORA_CHECK_OBSERVER_SHA256"] != ResourceObserverSHA256() ||
		environment["CHORA_CHECK_OBSERVER_HELPER"] != fixture.helperPath ||
		environment["CHORA_CHECK_OBSERVER_HELPER_SHA256"] != fixture.binding.HelperSHA256 ||
		environment["CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT"] != hex.EncodeToString(preparation.RuntimeFingerprint.Digest[:]) {
		t.Fatalf("observer environment = %#v", environment)
	}
	var config struct {
		Schema    string            `json:"schema"`
		AttemptID string            `json:"attemptId"`
		TaskRoot  string            `json:"taskRoot"`
		Resources []json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(configBody, &config); err != nil || config.Schema != "chora.resource-observer-config.v1" ||
		config.AttemptID != attempt.ID().String() || config.TaskRoot != root || len(config.Resources) != 1 {
		t.Fatalf("observer config = %#v err=%v", config, err)
	}
	contractFingerprint, err := fixture.adapter.FingerprintForContract(context.Background(), attempt.AgentExecutionProfileBinding(), contract)
	if err != nil || contractFingerprint != preparation.RuntimeFingerprint {
		t.Fatalf("contract fingerprint = %#v preparation=%#v err=%v", contractFingerprint, preparation.RuntimeFingerprint, err)
	}
}

func TestResourceObserverV12FailsClosedOnSourceOrHelperDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(resourceObserverAdapterFixture) error
		want   string
	}{
		{name: "source", mutate: func(f resourceObserverAdapterFixture) error {
			return os.WriteFile(f.binding.ExtensionPath, []byte("changed observer"), 0o600)
		}, want: "source identity changed"},
		{name: "helper", mutate: func(f resourceObserverAdapterFixture) error {
			return os.WriteFile(f.helperPath, []byte("changed helper"), 0o700)
		}, want: "helper identity changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResourceObserverAdapterFixture(t)
			run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
			contract := resourceObserverV12Contract(t, run)
			if err := test.mutate(fixture); err != nil {
				t.Fatal(err)
			}
			_, err := fixture.adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
				Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
				WorkspaceRoot: filepath.Join(fixture.root, "task-root"), LaunchToken: execution.LaunchToken{Value: "observer-drift"},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("drift error = %v want %q", err, test.want)
			}
		})
	}
}

func TestResourceObserverLeavesV11PathInvocationAndFingerprintUnchanged(t *testing.T) {
	fixture := newResourceObserverAdapterFixture(t)
	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v11","task":{"id":"` + run.TaskID().String() + `"}}`)
	base, err := fixture.adapter.FingerprintForBinding(context.Background(), attempt.AgentExecutionProfileBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.helperPath, []byte("drift ignored by v11"), 0o700); err != nil {
		t.Fatal(err)
	}
	preparation, err := fixture.adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
		Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract,
		WorkspaceRoot: filepath.Join(fixture.root, "legacy-root"), LaunchToken: execution.LaunchToken{Value: "observer-v11"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArguments := []string{"--mode", "rpc", "--session-dir", filepath.Join(fixture.root, "sessions", attempt.ID().String())}
	if preparation.RuntimeFingerprint != base || !reflect.DeepEqual(preparation.Invocation.Arguments(), wantArguments) {
		t.Fatalf("legacy preparation fingerprint=%#v base=%#v arguments=%#v", preparation.RuntimeFingerprint, base, preparation.Invocation.Arguments())
	}
	for key := range preparation.Invocation.Environment() {
		if strings.HasPrefix(key, "CHORA_CHECK_OBSERVER_") {
			t.Fatalf("legacy invocation contains observer environment %q", key)
		}
	}
}

func TestResourceObserverV12ResumeRequiresAndReusesExactContractFingerprint(t *testing.T) {
	fixture := newResourceObserverAdapterFixture(t)
	run, first, _ := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	contract := resourceObserverV12Contract(t, run)
	fingerprint, err := fixture.adapter.FingerprintForContract(context.Background(), first.AgentExecutionProfileBinding(), contract)
	if err != nil {
		t.Fatal(err)
	}
	predecessor := first.ID()
	sessionID := "123e4567-e89b-12d3-a456-426614174000"
	delta := `{"instructions":"continue exact v12 task"}`
	resumedAttempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2, Predecessor: &predecessor,
		ContextSnapshotID: first.ContextSnapshotID(), ContextDigest: first.ContextDigest(), AdapterID: AdapterID,
		AgentExecutionProfileBinding: first.AgentExecutionProfileBinding(), ContextDelta: delta,
		ExternalSession: sessionID, CreatedAt: first.CreatedAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	security := sha256.Sum256([]byte("observer-resume-security"))
	workingRoot := filepath.Join(fixture.root, "task-root")
	request := execution.ResumeRequest{
		ExecutionContractDocument: contract, Run: run, Attempt: resumedAttempt,
		Binding:             execution.ResumeBinding{ExternalSession: sessionID, SessionOwnerAttemptID: first.ID(), RuntimeFingerprint: fingerprint, WorkingRoot: workingRoot, SecurityFingerprint: security},
		SecurityFingerprint: security, DeltaInstruction: []byte(delta), LaunchToken: execution.LaunchToken{Value: "observer-resume"},
	}
	withoutContract := request
	withoutContract.ExecutionContractDocument = nil
	if _, err := fixture.adapter.PrepareResume(context.Background(), withoutContract); err == nil {
		t.Fatal("v12 resume accepted the base runtime fingerprint without its execution contract")
	}
	invocation, err := fixture.adapter.PrepareResume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantArguments := []string{"--mode", "rpc", "--extension", fixture.binding.ExtensionPath, "--session-dir", filepath.Join(fixture.root, "sessions", first.ID().String()), "--session", sessionID}
	if !reflect.DeepEqual(invocation.Arguments(), wantArguments) {
		t.Fatalf("resume arguments = %#v want %#v", invocation.Arguments(), wantArguments)
	}
	environment := invocation.Environment()
	if environment["CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT"] != hex.EncodeToString(fingerprint.Digest[:]) ||
		environment["CHORA_CHECK_OBSERVER_CONFIG"] != filepath.Join(filepath.Dir(fixture.binding.ExtensionPath), resumedAttempt.ID().String()+".json") {
		t.Fatalf("resume observer environment = %#v", environment)
	}
	configBody, err := os.ReadFile(environment["CHORA_CHECK_OBSERVER_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		AttemptID string `json:"attemptId"`
		TaskRoot  string `json:"taskRoot"`
	}
	if err := json.Unmarshal(configBody, &config); err != nil || config.AttemptID != resumedAttempt.ID().String() || config.TaskRoot != workingRoot {
		t.Fatalf("resume observer config = %#v err=%v", config, err)
	}
}

func TestResourceObserverV12FreshRetryRejectsInheritedSession(t *testing.T) {
	fixture := newResourceObserverAdapterFixture(t)
	run, first, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	predecessor := first.ID()
	for _, inherited := range []string{"123e4567-e89b-12d3-a456-426614174000", ""} {
		successor, err := domain.NewAttempt(domain.AttemptParams{
			ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2, Predecessor: &predecessor,
			ContextSnapshotID: first.ContextSnapshotID(), ContextDigest: first.ContextDigest(), AdapterID: AdapterID,
			AgentExecutionProfileBinding: first.AgentExecutionProfileBinding(), ExternalSession: inherited,
			ContextDelta: `{"instructions":"retry with bounded output"}`, CreatedAt: first.CreatedAt(),
		})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := fixture.adapter.PrepareBoundStart(context.Background(), execution.StartRequest{
			Run: run, Attempt: successor, SnapshotDocument: snapshot, ExecutionContractDocument: resourceObserverV12Contract(t, run),
			WorkspaceRoot: filepath.Join(fixture.root, "task-root"), LaunchToken: execution.LaunchToken{Value: "fresh-retry"},
		})
		if inherited != "" {
			if err == nil || !strings.Contains(err.Error(), "invalid Pi start identity") {
				t.Fatalf("inherited identity err=%v", err)
			}
		} else if err != nil || !prepared.Valid() {
			t.Fatalf("fresh successor preparation failed: %v", err)
		}
	}
}
