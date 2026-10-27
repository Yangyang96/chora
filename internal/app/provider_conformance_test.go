package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type syntheticProvider struct {
	fixtureDatabase   string
	omitIdentityProof bool
	completionError   bool
	*fakeAdapter
	completions   int
	recoveries    int
	stopOnRestart bool
}

// Independently observed fixture identities; never derive them from the binding.
func (p *syntheticProvider) Fingerprint(ctx context.Context) (execution.RuntimeFingerprint, error) {
	fingerprint, err := p.fakeAdapter.Fingerprint(ctx)
	fingerprint.SourceIdentity = sha256.Sum256([]byte("synthetic-executable"))
	fingerprint.SecurityFingerprint = sha256.Sum256([]byte("synthetic-security-policy"))
	return fingerprint, err
}
func (p *syntheticProvider) PrepareBoundStart(ctx context.Context, request execution.StartRequest) (execution.StartPreparation, error) {
	invocation, err := p.PrepareStart(ctx, request)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	fingerprint, err := p.Fingerprint(ctx)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	if p.omitIdentityProof {
		fingerprint.SourceIdentity = [32]byte{}
		fingerprint.SecurityFingerprint = [32]byte{}
	}
	return execution.NewStartPreparation(invocation, fingerprint)
}
func (p *syntheticProvider) Describe() execution.ProviderDescriptor {
	return execution.ProviderDescriptor{ID: p.ID(), Name: "Synthetic", Features: []string{"runtime.agent_loop", "runtime.jsonl_rpc", "runtime.file_edit", "runtime.command_execution", "runtime.progress_events", "runtime.terminal_result", "runtime.cooperative_abort", "runtime.allowlisted_event_projection", "runtime.chora_owned_result", "runtime.zero_implicit_context"}, ProtocolVersion: "synthetic.v1", Environments: []string{domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code"}, ResumeMode: execution.ResumeUnsupported, ShutdownMode: "exit_after_input", ApprovalMode: "native_noninteractive", ModelMode: "native_default", CheckEvidenceMode: "none"}
}
func (p *syntheticProvider) Complete(r execution.CompletionRequest) (execution.TerminalResult, error) {
	p.completions++
	if p.completionError {
		return execution.TerminalResult{}, errors.New("synthetic decode failure")
	}
	found := false
	for _, e := range r.Events {
		if e.Type() == "synthetic.complete" && e.Source() == "adapter" {
			found = true
		}
	}
	if !found {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "synthetic_incomplete"}, nil
	}
	return execution.TerminalResult{Kind: execution.TerminalReviewReady, Summary: "synthetic result"}, nil
}
func (p *syntheticProvider) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	p.recoveries++
	return execution.RecoveryPolicy{FreshRetry: true, MissingRouteUncertain: true, StopOnRestart: p.stopOnRestart}
}
func newSyntheticFixtureWithMutation(t *testing.T, p *syntheticProvider, s *fakeSupervisor, mutate func(*domain.RuntimeBindingV1)) (*app.Service, *sqlite.Store, domain.AgentRun) {
	provider, supervisor := p, s
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	p.fixtureDatabase = filepath.Join(root, "chora.db")
	db, err := openLatestSQLiteTestStore(ctx, p.fixtureDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := agent.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{
		Agents: registry, Supervisor: supervisor,
		Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
	})
	v4 := readSpecCodingContract(t, "chora-m1-real-task.v4.json")
	entryID := mustContextEntryID(t, "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab")
	revisionID := mustContextRevisionID(t, "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab")
	charterID := mustCharterID(t, "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab")
	frozenAt := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	workspaceRoot := filepath.Join(t.TempDir(), "source-baseline-v2", "repo")

	descriptor := provider.Describe()
	fingerprint, _ := provider.Fingerprint(ctx)
	bindingSpec := domain.RuntimeBindingV1{SchemaVersion: "chora.runtime-binding.v1", RuntimeID: provider.ID(), ProtocolVersion: descriptor.ProtocolVersion, SourceIdentity: fmt.Sprintf("%x", fingerprint.SourceIdentity), ConfigurationFingerprint: fmt.Sprintf("%x", fingerprint.Digest), ExecutionProvider: domain.TrustedHostExecutionProvider, SecurityFingerprint: fmt.Sprintf("%x", fingerprint.SecurityFingerprint), CapabilityDigest: fmt.Sprintf("%x", descriptor.Digest()), ModelProvenance: "native_default"}
	if mutate != nil {
		mutate(&bindingSpec)
	}
	runtimeBinding, err := registry.Bind(provider.ID(), bindingSpec, "code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AcknowledgeTrustedLocal(ctx, app.AcknowledgeTrustedLocalRequest{CommandMeta: meta("ack-provider", "ack-provider"), PolicyVersion: domain.TrustedLocalDisclosurePolicy}); err != nil {
		t.Fatal(err)
	}
	materialized, err := service.MaterializeSpecCodingContract(ctx, app.MaterializeSpecCodingContractRequest{
		RuntimeBinding: runtimeBinding, AgentAdapter: provider.ID(), CommandMeta: meta("materialize-spec-coding", "materialize-spec-coding"), Contract: v4,
		WorkspaceRoot: workspaceRoot, ContextEntryID: entryID, ContextRevisionID: revisionID,
		CharterID: charterID, FrozenAt: frozenAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(materialized.Snapshot.CanonicalJSON())
	if digest != materialized.Snapshot.Digest() || hex.EncodeToString(digest[:]) == speccoding.SupersededPlaceholderContextDigest {
		t.Fatalf("materialized digest = %x", digest)
	}
	if materialized.Task.ID().String() != v4.Document().Task.ID || materialized.Snapshot.ID().String() != v4.Document().Execution.Input.ContextSnapshotID {
		t.Fatalf("materialized identity task=%s snapshot=%s", materialized.Task.ID(), materialized.Snapshot.ID())
	}
	binding, err := db.Reader().GetSpecCodingBinding(ctx, materialized.Task.ID())
	if err != nil || binding.Status != "materialized" || binding.SnapshotDigest != digest {
		t.Fatalf("materialized binding = %#v, err=%v", binding, err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta("submit-spec-coding-plan", "submit-spec-coding-plan"), DraftID: materialized.Draft.ID(), ExpectedEditVersion: materialized.Draft.EditVersion()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("reject-unregistered-plan-acceptance", "reject-unregistered-plan-acceptance"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Must remain unaccepted until registration."}); !errors.Is(err, storecontract.ErrPlanningConflict) {
		t.Fatalf("materialized-only contract accepted for execution: %v", err)
	}

	v5Document := v4.Document()
	v5Document.SchemaVersion = speccoding.CoreContractSchemaVersionV5
	v5Document.Revision = 5
	v5Document.Execution.Input.ContextSnapshotDigest = hex.EncodeToString(digest[:])
	v5JSON, err := json.Marshal(v5Document)
	if err != nil {
		t.Fatal(err)
	}
	v5, err := speccoding.DecodeCoreContract(v5JSON)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterSpecCodingContract(ctx, app.RegisterSpecCodingContractRequest{
		CommandMeta: meta("register-spec-coding", "register-spec-coding"), Contract: v5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Binding.Status != "registered" || registered.Binding.ActiveContractDigest != sha256.Sum256(v5.CanonicalJSON()) {
		t.Fatalf("registered binding = %#v", registered.Binding)
	}
	accepted, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("accept-spec-coding-plan", "accept-spec-coding-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Registered contract plan accepted."})
	if err != nil || accepted.Binding == nil {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	createdRun, err := service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("create-spec-coding-run", "create-spec-coding-run"), TaskID: materialized.Task.ID(), RevisionID: submitted.Revision.ID()})
	if err != nil {
		t.Fatal(err)
	}

	return service, db, createdRun.Run
}

func TestSyntheticProviderLifecycleWithoutRuntimeBranches(t *testing.T) {
	for _, mode := range []string{"terminal", "stop", "restart", "failure", "exit_failure", "incomplete", "restart_alive", "provider_error"} {
		t.Run(mode, func(t *testing.T) {
			p := &syntheticProvider{fakeAdapter: &fakeAdapter{id: "synthetic", resumeMode: execution.ResumeUnsupported}}
			s := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}}}
			service, db, run := newSyntheticFixture(t, p, s)
			ctx := context.Background()
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("synthetic-prepare", "synthetic-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
			if err != nil || restored.AgentExecutionProfileBinding() != prepared.Attempt.AgentExecutionProfileBinding() {
				t.Fatalf("binding round trip: %v", err)
			}
			if mode == "failure" {
				p.fingerprint = execution.RuntimeFingerprint{Digest: [32]byte{9}, Version: "changed"}
			}
			started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("synthetic-start", "synthetic-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "failure" {
				if s.startCalls != 0 || started.Run.State() != domain.RunStateRecoveryRequired {
					t.Fatalf("identity drift launched child: %d %s", s.startCalls, started.Run.State())
				}
				return
			}
			if s.startCalls != 1 || p.prepareCalls != 1 {
				t.Fatal("provider not prepared and started exactly once")
			}
			s.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: 0, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
			switch mode {
			case "terminal":
				p.decode = []execution.NormalizedEvent{{Type: "synthetic.complete", NormalizedJSON: []byte(`{"complete":true}`)}, {Type: "agent_settled", NormalizedJSON: []byte(`{}`)}}
				s.drainOutcome.Chunks[execution.StreamStdout] = []byte("done\n")
				s.drainOutcome.Offsets[execution.StreamStdout] = 5
				s.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: s.invocation.LaunchToken()}
				s.sink.Exited()
				terminalResult, exitErr := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("synthetic-exit", "synthetic-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
				if exitErr != nil || p.completions != 1 || s.finalizeCalls != 1 || s.closeStdinCalls != 0 || terminalResult.Run.State() != domain.RunStateAwaitingVerification {
					t.Fatalf("complete=%d finalize=%d state=%s err=%v", p.completions, s.finalizeCalls, terminalResult.Run.State(), exitErr)
				}
			case "exit_failure", "incomplete", "provider_error":
				p.completionError = mode == "provider_error"
				if mode == "exit_failure" {
					s.drainOutcome.TerminalFiles.ExitCode = 1
				}
				s.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: s.invocation.LaunchToken()}
				s.sink.Exited()
				result, exitErr := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("synthetic-failed-exit", "synthetic-failed-exit"), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
				if exitErr != nil || result.Run.State() != domain.RunStateRecoveryRequired || s.finalizeCalls != 1 {
					t.Fatalf("failure state=%s finalized=%d err=%v", result.Run.State(), s.finalizeCalls, exitErr)
				}
				if mode == "exit_failure" && p.completions != 0 {
					t.Fatal("provider completion overrode trusted failed exit")
				}
			case "stop":
				_, err = service.RequestCancel(ctx, app.StopRequest{CommandMeta: meta("synthetic-stop", "synthetic-stop"), RunID: run.ID(), ExpectedVersion: started.Run.Version(), Reason: "test"})
				if err != nil || s.stopCalls != 1 || s.finalizeCalls != 1 {
					t.Fatalf("stop=%d finalize=%d err=%v", s.stopCalls, s.finalizeCalls, err)
				}
			case "restart", "restart_alive":
				if mode == "restart_alive" {
					p.stopOnRestart = true
					s.reconcileOutcomes = []execution.ReconcileOutcome{{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle"}}, {Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}}}
				}
				s.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: s.invocation.LaunchToken()}
				if err = service.RecoverStartup(ctx); err != nil || p.recoveries == 0 || s.finalizeCalls != 1 || (mode == "restart_alive" && s.stopCalls != 1) {
					t.Fatalf("recover=%d finalize=%d err=%v", p.recoveries, s.finalizeCalls, err)
				}
			}
		})
	}
}

func TestUnknownRuntimeFailsBeforeAllocatingAttempt(t *testing.T) {
	f := newFixture(t)
	service := app.NewService(app.Dependencies{Store: f.db, Context: app.ContextAssembler{}, Agents: agent.NewRegistry(), Supervisor: f.supervisor, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now()}, IDs: app.RandomIDs{}})
	_, err := service.PrepareRun(context.Background(), app.PrepareRunRequest{CommandMeta: meta("unknown-runtime", "unknown-runtime"), RunID: f.run.ID(), ExpectedVersion: f.run.Version()})
	if !errors.Is(err, execution.ErrUnknownRuntime) {
		t.Fatalf("unknown runtime: %v", err)
	}
	if _, err := f.db.Reader().GetCurrentAttempt(context.Background(), f.run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("allocated attempt: %v", err)
	}
	if f.supervisor.startCalls != 0 {
		t.Fatal("unknown runtime started")
	}
}

func newSyntheticFixture(t *testing.T, p *syntheticProvider, s *fakeSupervisor) (*app.Service, *sqlite.Store, domain.AgentRun) {
	return newSyntheticFixtureWithMutation(t, p, s, nil)
}
func TestFrozenIdentityMismatchMustNotLaunch(t *testing.T) {
	for _, field := range []string{"source", "security", "configuration", "missing_proof"} {
		t.Run(field, func(t *testing.T) {
			p := &syntheticProvider{fakeAdapter: &fakeAdapter{id: "synthetic", resumeMode: execution.ResumeUnsupported}}
			sup := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}}}
			service, db, run := newSyntheticFixtureWithMutation(t, p, sup, func(b *domain.RuntimeBindingV1) {
				mismatch := fmt.Sprintf("%x", [32]byte{9})
				switch field {
				case "source":
					b.SourceIdentity = mismatch
				case "security":
					b.SecurityFingerprint = mismatch
				case "configuration":
					b.ConfigurationFingerprint = mismatch
				}
			})
			p.omitIdentityProof = field == "missing_proof"
			prepared, err := service.PrepareRun(context.Background(), app.PrepareRunRequest{CommandMeta: meta("review-prepare", "review-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatalf("identity fixture did not reach launch admission: %v", err)
			}
			started, err := service.StartAttempt(context.Background(), app.StartAttemptRequest{CommandMeta: meta("review-start", "review-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil || sup.startCalls != 0 || p.prepareCalls != 1 || started.Run.State() != domain.RunStateRecoveryRequired {
				t.Fatalf("mismatched %s identity reached Supervisor.Start: calls=%d state=%s err=%v", field, sup.startCalls, started.Run.State(), err)
			}
			session, err := db.Reader().GetRuntimeSessionForAttempt(context.Background(), started.Attempt.ID())
			if err != nil || session.State != "failed" || session.FinalizedAt.IsZero() {
				t.Fatalf("rejected launch was not finalized: state=%s error=%v", session.State, err)
			}
		})
	}
}

func TestProviderCannotBypassTrustedExitOrCancel(t *testing.T) {
	for _, mode := range []string{"no_exit_signal", "still_alive", "cancel_confirmed", "cancel_uncertain", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			p := &syntheticProvider{fakeAdapter: &fakeAdapter{id: "synthetic", resumeMode: execution.ResumeUnsupported}}
			sup := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}}}
			service, db, run := newSyntheticFixture(t, p, sup)
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("guard-prepare", "guard-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("guard-start", "guard-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
			if err != nil {
				t.Fatal(err)
			}
			p.decode = []execution.NormalizedEvent{{Type: "synthetic.complete", NormalizedJSON: []byte(`{"complete":true}`)}}
			sup.drainOutcome = execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{execution.StreamStdout: []byte("done\n")}, Offsets: execution.StreamOffsets{execution.StreamStdout: 5, execution.StreamStderr: 0}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}}
			expected := started.Run.Version()
			switch mode {
			case "cancel_confirmed", "cancel_uncertain":
				if mode == "cancel_uncertain" {
					sup.stopOutcome = execution.StopOutcome{Kind: execution.StopUncertain}
				}
				stopped, e := service.RequestCancel(ctx, app.StopRequest{CommandMeta: meta("guard-cancel", "guard-cancel"), RunID: run.ID(), ExpectedVersion: expected, Reason: "cancel before apparent completion"})
				if e != nil {
					t.Fatal(e)
				}
				expected = stopped.Run.Version()
			case "deadline":
				sup.drainOutcome.TerminalFiles.TerminationCause = execution.TerminationDeadlineExceeded
			}
			sup.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}}
			if mode == "still_alive" {
				sup.reconcileOutcome.Kind = execution.ReconcileAlive
			}
			if mode != "no_exit_signal" {
				sup.sink.Exited()
			}
			_, exitErr := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta("guard-exit", "guard-exit"), SessionID: started.Session.ID, ExpectedVersion: expected})
			actual, e := db.Reader().GetRun(ctx, run.ID())
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "no_exit_signal", "still_alive":
				if exitErr == nil || p.completions != 0 || actual.State() != domain.RunStateRunning {
					t.Fatalf("exit proof bypassed: state=%s completes=%d err=%v", actual.State(), p.completions, exitErr)
				}
			case "cancel_confirmed":
				if actual.State() != domain.RunStateCancelled {
					t.Fatalf("cancel overwritten: %s err=%v", actual.State(), exitErr)
				}
			case "cancel_uncertain":
				if actual.State() != domain.RunStateRecoveryRequired {
					t.Fatalf("uncertain cancel overwritten: %s err=%v", actual.State(), exitErr)
				}
			case "deadline":
				if exitErr != nil || p.completions != 0 || actual.State() != domain.RunStateRecoveryRequired {
					t.Fatalf("deadline bypassed: state=%s completes=%d err=%v", actual.State(), p.completions, exitErr)
				}
			}
		})
	}
}

// No ownership outcome must remain uncertainty through the real composition
// entry, not merely through app's test supervisor or a router unit fixture.
type unownedProviderSupervisor struct{ *fakeSupervisor }

// The core must preserve unknown ownership even if a provider has no optional
// missing-route policy; provider callbacks cannot authorize a replacement.
type defaultRecoveryProvider struct{ *syntheticProvider }

func (p defaultRecoveryProvider) Recover(execution.RecoveryRequest) execution.RecoveryPolicy {
	return execution.RecoveryPolicy{FreshRetry: true}
}

func (s unownedProviderSupervisor) Reconcile(context.Context, execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain}, nil
}
func (s unownedProviderSupervisor) ReconcileLaunch(context.Context, execution.LaunchToken) (execution.ReconcileOutcome, error) {
	return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain}, nil
}
func TestProviderOwnershipLossPreservesWorkbenchRestartAPI(t *testing.T) {
	ctx := context.Background()
	provider := &syntheticProvider{fakeAdapter: &fakeAdapter{id: "synthetic", resumeMode: execution.ResumeUnsupported}}
	supervisor := &fakeSupervisor{outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "owned"}, Identity: execution.ProcessIdentity{Value: "lost-process"}}}
	service, db, run := newSyntheticFixture(t, provider, supervisor)
	prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("restart-prepare", "restart-prepare"), RunID: run.ID(), ExpectedVersion: run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta("restart-start", "restart-start"), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	web := filepath.Join(source, "web")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := unownedProviderSupervisor{&fakeSupervisor{}}
	server, err := localweb.NewWorkbench(ctx, provider.fixtureDatabase, web, log.New(io.Discard, "", 0), localweb.WorkbenchOptions{SourceRoot: source, DataRoot: filepath.Dir(provider.fixtureDatabase), RuntimeProviders: []localweb.RuntimeProviderRegistration{{Provider: defaultRecoveryProvider{provider}, Supervisors: map[string]execution.ProcessSupervisor{"trusted_host": missing}}}})
	if err != nil {
		t.Fatalf("restart must retain readable recovery surface: %v", err)
	}
	defer server.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID().String(), nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("run unreadable: %d %s", response.Code, response.Body.String())
	}
	var view struct {
		State    string `json:"status"`
		Controls struct {
			CanRetry bool `json:"canRetry"`
		} `json:"controls"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != string(domain.RunStateRecoveryRequired) || view.Controls.CanRetry {
		t.Fatalf("unsafe recovery view: %s", response.Body.String())
	}
	restored, err := sqlite.Open(ctx, provider.fixtureDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	session, err := restored.Reader().GetRuntimeSessionForAttempt(ctx, started.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !session.FinalizedAt.IsZero() || missing.stopCalls != 0 || missing.startCalls != 0 || missing.finalizeCalls != 0 {
		t.Fatal("unowned process was signalled, finalized or replaced")
	}
	registry := agent.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	recoveryService := app.NewService(app.Dependencies{Agents: registry, Supervisor: missing, Store: restored, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now()}, IDs: app.RandomIDs{}})
	recovered, err := restored.Reader().GetRun(ctx, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryService.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("forbidden-successor", "forbidden-successor"), RunID: run.ID(), ExpectedVersion: recovered.Version()}); err == nil {
		t.Fatal("unowned process allowed successor")
	}
}
