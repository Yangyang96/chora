package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const proposalTestText = "# Proposed research\n```chora-delegation-plan\n" + `{"schemaVersion":"chora.delegation-plan.v1","assignments":[{"role":"Researcher","title":"Compare","requirement":"Use supplied material."}]}` + "\n```"

// This port double deliberately implements no document-review or runtime
// methods. Proposal preview/import must not invoke either authority surface.
type proposalTestPort struct {
	storecontract.WriteTx
	task        domain.Task
	room        domain.Room
	project     domain.Project
	run         domain.AgentRun
	attempt     domain.Attempt
	report      domain.AgentReport
	group       domain.ResourceResultGroup
	events      []domain.RunEvent
	resources   storecontract.TaskResourceRecord
	settings    domain.TaskExecutionSettings
	binding     storecontract.SpecCodingBinding
	delegation  *domain.TaskDelegation
	source      *domain.DelegationProposalSource
	commands    map[[32]byte]storecontract.CommandKey
	beforeWrite func()
	failSource  bool
}
type proposalTestStore struct {
	storecontract.Store
	port *proposalTestPort
}

func (s proposalTestStore) Reader() storecontract.Reader { return s.port }
func (s proposalTestStore) WithinWriteTx(ctx context.Context, f func(storecontract.WriteTx) error) error {
	if s.port.beforeWrite != nil {
		s.port.beforeWrite()
		s.port.beforeWrite = nil
	}
	oldD, oldS := s.port.delegation, s.port.source
	err := f(s.port)
	if err != nil {
		s.port.delegation, s.port.source = oldD, oldS
	}
	return err
}

type proposalTestAuthorizer struct{}

func (proposalTestAuthorizer) Authorize(context.Context, CommandAuthorizationRequest) error {
	return nil
}
func (p *proposalTestPort) GetTask(_ context.Context, id domain.TaskID) (domain.Task, error) {
	if id != p.task.ID() {
		return domain.Task{}, storecontract.ErrNotFound
	}
	return p.task, nil
}
func (p *proposalTestPort) GetRoom(context.Context, domain.RoomID) (domain.Room, error) {
	return p.room, nil
}
func (p *proposalTestPort) GetProject(context.Context, domain.ProjectID) (domain.Project, error) {
	return p.project, nil
}
func (p *proposalTestPort) GetTaskResourceSnapshot(context.Context, domain.TaskID) (storecontract.TaskResourceRecord, error) {
	return p.resources, nil
}
func (p *proposalTestPort) GetTaskExecutionSettings(context.Context, domain.TaskID) (domain.TaskExecutionSettings, error) {
	return p.settings, nil
}
func (p *proposalTestPort) GetDelegationChild(context.Context, domain.TaskID) (domain.DelegationChild, error) {
	return domain.DelegationChild{}, storecontract.ErrNotFound
}
func (p *proposalTestPort) GetTaskDelegation(context.Context, domain.TaskID) (domain.TaskDelegation, error) {
	if p.delegation == nil {
		return domain.TaskDelegation{}, storecontract.ErrNotFound
	}
	return *p.delegation, nil
}
func (p *proposalTestPort) GetDelegationProposalSource(context.Context, domain.TaskID) (domain.DelegationProposalSource, error) {
	if p.source == nil {
		return domain.DelegationProposalSource{}, storecontract.ErrNotFound
	}
	return *p.source, nil
}
func (p *proposalTestPort) ListDelegationChildren(context.Context, domain.TaskID) ([]domain.DelegationChild, error) {
	return nil, nil
}
func (p *proposalTestPort) ListTaskRunHistory(context.Context, domain.RoomID, domain.TaskID) ([]storecontract.RunSummary, error) {
	return []storecontract.RunSummary{{Run: p.run}}, nil
}
func (p *proposalTestPort) GetCurrentAttempt(context.Context, domain.RunID) (domain.Attempt, error) {
	return p.attempt, nil
}
func (p *proposalTestPort) GetResourceResultGroup(context.Context, domain.AttemptID) (domain.ResourceResultGroup, error) {
	return p.group, nil
}
func (p *proposalTestPort) GetAgentReportForAttempt(context.Context, domain.AttemptID) (domain.AgentReport, error) {
	return p.report, nil
}
func (p *proposalTestPort) ListRunEvents(context.Context, domain.RunID) ([]domain.RunEvent, error) {
	return p.events, nil
}
func (p *proposalTestPort) GetSpecCodingBinding(context.Context, domain.TaskID) (storecontract.SpecCodingBinding, error) {
	return p.binding, nil
}
func (p *proposalTestPort) LookupCommand(_ context.Context, k storecontract.CommandKey) (storecontract.Response, bool, error) {
	old, ok := p.commands[k.KeyHash]
	if ok && old.RequestDigest != k.RequestDigest {
		return storecontract.Response{}, false, storecontract.ErrIdempotencyConflict
	}
	return storecontract.Response{Body: []byte(`{}`)}, ok, nil
}
func (p *proposalTestPort) SaveCommand(_ context.Context, k storecontract.CommandKey, _ storecontract.Response) error {
	p.commands[k.KeyHash] = k
	return nil
}
func (p *proposalTestPort) InsertTaskDelegation(_ context.Context, d domain.TaskDelegation) error {
	if err := d.Validate(); err != nil {
		return err
	}
	p.delegation = &d
	return nil
}
func (p *proposalTestPort) InsertDelegationProposalSource(_ context.Context, s domain.DelegationProposalSource) error {
	if p.failSource {
		return errors.New("injected source persistence failure")
	}
	if err := s.Validate(); err != nil {
		return err
	}
	p.source = &s
	return nil
}

func newProposalTestService(t *testing.T) (*Service, *proposalTestPort) {
	t.Helper()
	p := &proposalTestPort{commands: map[[32]byte]storecontract.CommandKey{}}
	now := time.Now().UTC()
	pid, rid := domain.NewProjectID(), domain.NewRoomID()
	var err error
	p.project, err = domain.NewProject(domain.ProjectParams{ID: pid, Name: "Project", DefaultRoomID: rid, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	p.room, err = domain.NewRoom(domain.RoomParams{ID: rid, ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: "Room", WorkspaceRoot: "/tmp/proposal-test", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Research", "")
	if err != nil {
		t.Fatal(err)
	}
	p.task, err = domain.NewTask(domain.NewTaskID(), rid, "Research", "Research supplied material", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	p.run, err = domain.NewAgentRun(domain.NewRunID(), p.task.ID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []domain.CommandKind{domain.CommandPrepareRun, domain.CommandStartAttempt, domain.CommandSubmitAgentReportDirectReview} {
		p.run, err = p.run.Transition(cmd, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256([]byte("authority"))
	p.attempt, err = domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: p.run.ID(), Sequence: 1, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: digest, AdapterID: "fake", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	p.report, err = domain.NewAgentReport(domain.AgentReportParams{ID: domain.NewAgentReportID(), RunID: p.run.ID(), AttemptID: p.attempt.ID(), Summary: "Proposal", FinalText: proposalTestText, CompletedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"attempt.started", "assistant_message"} {
		source := "app"
		if i == 1 {
			source = "adapter"
		}
		raw, _ := json.Marshal(map[string]any{"text": proposalTestText, "terminal_complete": true})
		event, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: p.run.ID(), Sequence: int64(i + 1), Type: kind, Source: source, OccurredAt: now, RecordedAt: now, NormalizedJSON: raw})
		if err != nil {
			t.Fatal(err)
		}
		p.events = append(p.events, event)
	}
	materialDigest := sha256.Sum256([]byte("Supplied material"))
	snapshot := domain.TaskResourceSnapshot{SchemaVersion: domain.TaskResourceSchemaV2, TaskID: p.task.ID().String(), ProjectID: pid.String(), RoomID: rid.String(), SelectionSource: "user_start", OutcomeKind: "document", Resources: []domain.TaskRepositoryResource{}, Materials: []domain.TaskMaterial{{Title: "Design", Locator: "supplied:design", Body: "Supplied material", Digest: fmt.Sprintf("%x", materialDigest)}}}
	raw, rd, err := snapshot.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	p.resources = storecontract.TaskResourceRecord{TaskID: p.task.ID(), CanonicalJSON: raw, Digest: rd, CreatedAt: now}
	p.settings = domain.TaskExecutionSettings{TaskID: p.task.ID(), ProjectID: pid, AgentExecutionProfile: domain.AgentExecutionProfileIsolatedLocal, EnvironmentSource: domain.ExecutionSettingsSourceTask, ModelSource: domain.ExecutionSettingsSourceDefault, NativeCapabilitiesJSON: `{}`, CreatedAt: now}
	p.binding = storecontract.SpecCodingBinding{TaskID: p.task.ID(), ActiveContractDigest: digest, SnapshotDigest: digest}
	final, _, _ := domain.CurrentCompleteAssistantEvidence(p.events)
	p.group = domain.ResourceResultGroup{SchemaVersion: "chora.result-group.v2", ID: domain.NewResultID().String(), RunID: p.run.ID().String(), TaskID: p.task.ID().String(), AttemptID: p.attempt.ID().String(), AgentReportID: p.report.ID().String(), FinalAssistant: final, ResourceSnapshotDigest: fmt.Sprintf("%x", rd), ContractDigest: fmt.Sprintf("%x", digest), ContextDigest: fmt.Sprintf("%x", digest), Outcome: "review_ready", OutcomeKind: "document", Markdown: proposalTestText, CreatedAt: now}
	return NewService(Dependencies{Store: proposalTestStore{port: p}, Authorizer: proposalTestAuthorizer{}}), p
}

func proposalStartRequest(p *proposalTestPort) StartDelegationRequest {
	_, d, _ := p.group.CanonicalJSON()
	return StartDelegationRequest{CommandMeta: CommandMeta{ActorID: "local-human", SessionID: "browser", IdempotencyKey: "proposal-start"}, ParentTaskID: p.task.ID(), SourceAttemptID: p.attempt.ID(), ExpectedResultDigest: fmt.Sprintf("%x", d)}
}

func TestDelegationProposalImportFreezesSourceAndReplaysAfterParentChanges(t *testing.T) {
	s, p := newProposalTestService(t)
	ctx := context.Background()
	preview, err := s.GetDelegationProposal(ctx, p.task.ID())
	if err != nil || !preview.Available || preview.Source == nil || !preview.Source.Current {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if p.delegation != nil || p.source != nil || len(p.commands) != 0 {
		t.Fatal("preview mutated authority")
	}
	req := proposalStartRequest(p)
	v, err := s.StartDelegation(ctx, req)
	if err != nil || v.Source == nil || !v.Source.Current || !reflect.DeepEqual(v.Assignments, preview.Assignments) {
		t.Fatalf("start=%#v err=%v", v, err)
	}
	if p.source.RunID != p.run.ID() || p.source.EventID.String() != p.group.FinalAssistant.EventID || p.source.PlanDigest != domain.DelegationPlanDigest(preview.Assignments) {
		t.Fatal("incomplete source evidence")
	}
	frozen := *p.source
	p.events = p.events[:1] // The formerly current complete result is no longer provable.
	replayed, err := s.StartDelegation(ctx, req)
	if err != nil || replayed.Source == nil || replayed.Source.Current || !reflect.DeepEqual(replayed.Assignments, preview.Assignments) || *p.source != frozen {
		t.Fatalf("replay rebound source: %#v err=%v", replayed, err)
	}
	if len(p.commands) != 1 {
		t.Fatal("replay created a new command")
	}
}

func TestDelegationProposalRejectsStaleOrForeignSourceWithoutMutation(t *testing.T) {
	for _, name := range []string{"other-attempt", "digest", "assignments-and-source", "missing-digest", "short-digest", "nonhex-digest", "uppercase-digest", "cross-task", "latest-run", "resource-drift", "changed-before-tx", "incomplete-event", "report-mismatch", "plan-invalid", "source-write-failure"} {
		t.Run(name, func(t *testing.T) {
			s, p := newProposalTestService(t)
			req := proposalStartRequest(p)
			switch name {
			case "other-attempt":
				req.SourceAttemptID = domain.NewAttemptID()
			case "digest":
				req.ExpectedResultDigest = strings.Repeat("0", 64)
			case "assignments-and-source":
				req.Assignments = []domain.DelegationAssignment{}
			case "missing-digest":
				req.ExpectedResultDigest = ""
			case "short-digest":
				req.ExpectedResultDigest = "abcd"
			case "nonhex-digest":
				req.ExpectedResultDigest = strings.Repeat("z", 64)
			case "uppercase-digest":
				req.ExpectedResultDigest = strings.Repeat("A", 64)
			case "cross-task":
				p.group.TaskID = domain.NewTaskID().String()
			case "latest-run":
				p.run, _ = domain.NewAgentRun(domain.NewRunID(), p.task.ID(), domain.NewCharterID(), time.Now().UTC())
			case "resource-drift":
				p.binding.SnapshotDigest = sha256.Sum256([]byte("new authority"))
			case "changed-before-tx":
				p.beforeWrite = func() { p.group.ID = domain.NewResultID().String() }
			case "incomplete-event":
				p.events = p.events[:1]
			case "report-mismatch":
				p.report, _ = domain.NewAgentReport(domain.AgentReportParams{ID: p.report.ID(), RunID: p.run.ID(), AttemptID: p.attempt.ID(), FinalText: "different", CompletedAt: time.Now()})
			case "plan-invalid":
				p.group.Markdown = "not a plan"
			case "source-write-failure":
				p.failSource = true
			}
			if _, err := s.StartDelegation(context.Background(), req); err == nil {
				t.Fatal("invalid source started delegation")
			}
			if p.delegation != nil || p.source != nil || len(p.commands) != 0 {
				t.Fatal("failed import partially mutated state")
			}
		})
	}
}

func TestDelegationProposalUnavailableIsReadOnly(t *testing.T) {
	s, p := newProposalTestService(t)
	p.events = p.events[:1]
	v, err := s.GetDelegationProposal(context.Background(), p.task.ID())
	if err != nil || v.Available || v.Reason == "" || v.Source != nil || len(v.Assignments) != 0 {
		t.Fatalf("unavailable=%#v err=%v", v, err)
	}
	if p.delegation != nil || len(p.commands) != 0 {
		t.Fatal("unavailable preview mutated state")
	}
}

func TestDelegationAssignmentsModeKeepsOriginalCommandDigest(t *testing.T) {
	s, p := newProposalTestService(t)
	ctx := context.Background()
	a := []domain.DelegationAssignment{{Role: "Researcher", Title: "Work", Requirement: "Research"}}
	req := StartDelegationRequest{CommandMeta: CommandMeta{ActorID: "local-human", SessionID: "browser", IdempotencyKey: "legacy-start"}, ParentTaskID: p.task.ID(), Assignments: a}
	key, err := s.commandKey(req.CommandMeta, "start_delegation", p.task.ID().String(), 0, a, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartDelegation(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := p.commands[key.KeyHash]; got.RequestDigest != key.RequestDigest {
		t.Fatal("legacy command digest changed")
	}
	if p.source != nil {
		t.Fatal("manual assignments fabricated Agent provenance")
	}
}

func TestDelegationFrozenSourceRemainsCurrentAfterReview(t *testing.T) {
	for _, kind := range []domain.ReviewDecisionKind{domain.ReviewDecisionAccept, domain.ReviewDecisionReject} {
		t.Run(string(kind), func(t *testing.T) {
			s, p := newProposalTestService(t)
			ctx := context.Background()
			if _, err := s.StartDelegation(ctx, proposalStartRequest(p)); err != nil {
				t.Fatal(err)
			}
			frozen := *p.source
			authorization, err := domain.MintHumanReviewAuthorization(p.run.ID(), kind, p.run.Version(), true)
			if err != nil {
				t.Fatal(err)
			}
			params := domain.ReviewDecisionParams{ID: domain.NewReviewDecisionID(), RunID: p.run.ID(), ExpectedRunVersion: p.run.Version(), Comment: "Explicit test review", DecidedAt: time.Now().UTC()}
			var decision domain.ReviewDecision
			if kind == domain.ReviewDecisionAccept {
				decision, err = domain.NewAcceptedReviewDecision(params, authorization)
			} else {
				decision, err = domain.NewRejectedReviewDecision(params, authorization)
			}
			if err != nil {
				t.Fatal(err)
			}
			p.run, err = p.run.ApplyReview(decision)
			if err != nil {
				t.Fatal(err)
			}
			view, err := s.GetDelegation(ctx, p.task.ID())
			if err != nil || view.Source == nil || !view.Source.Current || *p.source != frozen {
				t.Fatalf("review changed source: %#v err=%v", view, err)
			}
			if _, _, err = loadDelegationProposal(ctx, p, p.task.ID(), p.attempt.ID(), fmt.Sprintf("%x", frozen.ResultDigest)); !errors.Is(err, ErrReviewEvidenceUnavailable) {
				t.Fatalf("reviewed source became importable: %v", err)
			}
		})
	}
}

func (p *proposalTestPort) GetDelegationPlanning(context.Context, domain.TaskID) (domain.DelegationPlanningIntent, error) {
	return domain.DelegationPlanningIntent{}, storecontract.ErrNotFound
}

func (p *proposalTestPort) GetDelegationSynthesis(context.Context, domain.TaskID) (domain.DelegationSynthesis, error) {
	return domain.DelegationSynthesis{}, storecontract.ErrNotFound
}
func (p *proposalTestPort) GetSynthesisForTask(context.Context, domain.TaskID) (domain.DelegationSynthesis, error) {
	return domain.DelegationSynthesis{}, storecontract.ErrNotFound
}
