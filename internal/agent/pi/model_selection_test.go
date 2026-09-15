package pi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestPathModelSelectionRevalidatesAndReachesInvocation(t *testing.T) {
	root := t.TempDir()
	source, err := NewPathPiSource(PathPiSourceParams{ExecutablePath: filepath.Join(root, "pi"), Version: "0.85.1", ExecutableSHA256: sha256.Sum256([]byte(fakePiBytes))})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := domain.NewModelCatalog("path-pi", fmt.Sprintf("%x", source.SourceIdentity()), source.Version(), []domain.ModelIdentity{{Provider: "p", ModelID: "chosen"}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	adapter, err := New(Config{PathPiSource: source, SessionRoot: filepath.Join(root, "sessions"), ReadSourceFile: func(string) ([]byte, error) { return []byte(fakePiBytes), nil }, ModelCatalog: func(context.Context, domain.AgentExecutionProfile) (domain.ModelCatalog, error) {
		calls++
		return catalog, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	run, initial, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileTrustedLocal)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: initial.ID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: initial.ContextSnapshotID(), ContextDigest: initial.ContextDigest(), AdapterID: AdapterID, AgentExecutionProfileBinding: initial.AgentExecutionProfileBinding(), ModelBinding: binding, CreatedAt: initial.CreatedAt()})
	if err != nil {
		t.Fatal(err)
	}
	request := execution.StartRequest{Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"` + run.TaskID().String() + `"}}`), WorkspaceRoot: filepath.Join(root, "ws"), LaunchToken: execution.LaunchToken{Value: "model-launch"}}
	invocation, err := adapter.PrepareStart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	args := invocation.Arguments()
	if !reflect.DeepEqual(args[len(args)-4:], []string{"--provider", "p", "--model", "chosen"}) || calls != 1 {
		t.Fatalf("model not applied: %v", args)
	}
	predecessor := attempt.ID()
	resumedAttempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2, Predecessor: &predecessor, ContextSnapshotID: attempt.ContextSnapshotID(), ContextDigest: attempt.ContextDigest(), AdapterID: AdapterID, AgentExecutionProfileBinding: attempt.AgentExecutionProfileBinding(), ModelBinding: binding, ExternalSession: "123e4567-e89b-12d3-a456-426614174000", ContextDelta: "retry", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	fp, err := adapter.FingerprintForBinding(context.Background(), attempt.AgentExecutionProfileBinding())
	if err != nil {
		t.Fatal(err)
	}
	security := sha256.Sum256([]byte("model-security"))
	resume := execution.ResumeRequest{Run: run, Attempt: resumedAttempt, Binding: execution.ResumeBinding{ExternalSession: resumedAttempt.ExternalSession(), SessionOwnerAttemptID: attempt.ID(), RuntimeFingerprint: fp, WorkingRoot: request.WorkspaceRoot, SecurityFingerprint: security}, SecurityFingerprint: security, DeltaInstruction: []byte("retry"), LaunchToken: execution.LaunchToken{Value: "resume-model"}}
	resumed, err := adapter.PrepareResume(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	resumedArgs := resumed.Arguments()
	if !reflect.DeepEqual(resumedArgs[len(resumedArgs)-4:], []string{"--provider", "p", "--model", "chosen"}) {
		t.Fatalf("resume lost model: %v", resumedArgs)
	}
	catalog, _ = domain.NewModelCatalog("path-pi", fmt.Sprintf("%x", source.SourceIdentity()), source.Version(), []domain.ModelIdentity{{Provider: "p", ModelID: "replacement"}})
	if _, err := adapter.PrepareStart(context.Background(), request); err == nil {
		t.Fatal("removed model silently accepted")
	}
	catalog, _ = domain.NewModelCatalog("path-pi", "changed-runtime", source.Version(), []domain.ModelIdentity{{Provider: "p", ModelID: "chosen"}})
	if _, err := adapter.PrepareStart(context.Background(), request); err == nil {
		t.Fatal("runtime drift accepted")
	}
	if _, err := adapter.PrepareResume(context.Background(), resume); err == nil {
		t.Fatal("resume accepted changed Runtime")
	}
	// Even a self-consistent binding and discovery for B cannot launch bound A.
	foreign, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	selectedSource, err := adapter.sourceForBinding(attempt.AgentExecutionProfileBinding())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.validateModelBinding(context.Background(), selectedSource, foreign); err == nil {
		t.Fatal("foreign binding and discovery launched the original executable")
	}
	if attempt.ModelBinding() != binding {
		t.Fatal("historical request changed")
	}
}

func TestExactModelArgumentsRejectRuntimeReferenceAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		selected domain.ModelIdentity
		models   []domain.ModelIdentity
		valid    bool
	}{
		{domain.ModelIdentity{Provider: "p", ModelID: "p/foo"}, []domain.ModelIdentity{{Provider: "p", ModelID: "foo"}, {Provider: "p", ModelID: "p/foo"}}, false},
		{domain.ModelIdentity{Provider: "p", ModelID: "foo"}, []domain.ModelIdentity{{Provider: "p", ModelID: "foo"}, {Provider: "p", ModelID: "FOO"}}, false},
		{domain.ModelIdentity{Provider: "p", ModelID: "foo"}, []domain.ModelIdentity{{Provider: "p", ModelID: "foo"}, {Provider: "P", ModelID: "bar"}}, false},
		{domain.ModelIdentity{Provider: "p", ModelID: "org/model:high"}, []domain.ModelIdentity{{Provider: "p", ModelID: "org/model:high"}}, true},
	} {
		catalog, err := domain.NewModelCatalog("path-pi", "runtime", "1", tc.models)
		if err != nil {
			t.Fatal(err)
		}
		if got := ValidateExactModelArguments(catalog, tc.selected); (got == nil) != tc.valid {
			t.Fatalf("selection %#v: %v", tc.selected, got)
		}
	}
}
