package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type DelegationProposalSourceView struct {
	RunID            string `json:"runId"`
	AttemptID        string `json:"attemptId"`
	ResultID         string `json:"resultId"`
	AgentReportID    string `json:"agentReportId"`
	EventID          string `json:"eventId"`
	EventSequence    int64  `json:"eventSequence"`
	ResultDigest     string `json:"resultDigest"`
	SourceTextDigest string `json:"sourceTextDigest"`
	PlanDigest       string `json:"planDigest"`
	Current          bool   `json:"current"`
}
type DelegationProposalView struct {
	Available   bool                          `json:"available"`
	Reason      string                        `json:"reason,omitempty"`
	Assignments []domain.DelegationAssignment `json:"assignments"`
	Source      *DelegationProposalSourceView `json:"source,omitempty"`
}

func delegationSourceView(s domain.DelegationProposalSource, current bool) DelegationProposalSourceView {
	return DelegationProposalSourceView{RunID: s.RunID.String(), AttemptID: s.AttemptID.String(), ResultID: s.ResultID.String(), AgentReportID: s.AgentReportID.String(), EventID: s.EventID.String(), EventSequence: s.EventSequence, ResultDigest: fmt.Sprintf("%x", s.ResultDigest), SourceTextDigest: fmt.Sprintf("%x", s.SourceTextDigest), PlanDigest: fmt.Sprintf("%x", s.PlanDigest), Current: current}
}

func (s *Service) GetDelegationProposal(ctx context.Context, id domain.TaskID) (DelegationProposalView, error) {
	v := DelegationProposalView{Assignments: []domain.DelegationAssignment{}}
	r := s.deps.Store.Reader()
	if _, err := r.GetTask(ctx, id); err != nil {
		return v, err
	}
	if _, _, err := delegationParent(ctx, r, id); err != nil {
		if delegationProposalUnavailable(err) {
			v.Reason = err.Error()
			return v, nil
		}
		return v, err
	}
	if _, err := r.GetTaskDelegation(ctx, id); err == nil {
		v.Reason = "A delegation is already frozen for this Task."
		return v, nil
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return v, err
	}
	assignments, source, err := loadDelegationProposal(ctx, r, id, domain.AttemptID{}, "")
	if err != nil {
		if delegationProposalUnavailable(err) {
			v.Reason = err.Error()
			return v, nil
		}
		return v, err
	}
	v.Available = true
	v.Assignments = assignments
	sourceView := delegationSourceView(source, true)
	v.Source = &sourceView
	return v, nil
}

func delegationProposalUnavailable(err error) bool {
	return errors.Is(err, storecontract.ErrNotFound) || errors.Is(err, storecontract.ErrRoomStateForbidden) || errors.Is(err, ErrInvalidCommand) || errors.Is(err, ErrReviewEvidenceUnavailable) || errors.Is(err, domain.ErrInvalidArgument)
}

// loadDelegationProposal reads only persisted parent evidence. Calling it again
// in the start transaction fences preview drift and concurrent retry/start.
func loadDelegationProposal(ctx context.Context, r storecontract.Reader, id domain.TaskID, expectedAttempt domain.AttemptID, expectedDigest string) ([]domain.DelegationAssignment, domain.DelegationProposalSource, error) {
	return loadDelegationProposalEvidence(ctx, r, id, expectedAttempt, expectedDigest, true)
}

// Existing frozen provenance remains current across review decisions; only a new
// source or changed evidence makes it stale. Import separately requires review readiness.
func loadDelegationProposalEvidence(ctx context.Context, r storecontract.Reader, id domain.TaskID, expectedAttempt domain.AttemptID, expectedDigest string, requireReviewReady bool) ([]domain.DelegationAssignment, domain.DelegationProposalSource, error) {
	var source domain.DelegationProposalSource
	bad := func(reason string) ([]domain.DelegationAssignment, domain.DelegationProposalSource, error) {
		return nil, source, fmt.Errorf("%w: %s", ErrReviewEvidenceUnavailable, reason)
	}
	task, err := r.GetTask(ctx, id)
	if err != nil {
		return nil, source, err
	}
	history, err := r.ListTaskRunHistory(ctx, task.RoomID(), id)
	if err != nil {
		return nil, source, err
	}
	if len(history) == 0 {
		return bad("Parent Task has no completed Agent proposal.")
	}
	run := history[0].Run
	if run.TaskID() != id || requireReviewReady && run.State() != domain.RunStateAwaitingReview {
		return bad("Parent proposal requires its latest Run to be awaiting review.")
	}
	attempt, err := r.GetCurrentAttempt(ctx, run.ID())
	if err != nil {
		return nil, source, err
	}
	if attempt.RunID() != run.ID() || expectedAttempt.Valid() && attempt.ID() != expectedAttempt {
		return bad("Parent proposal Attempt has changed.")
	}
	group, err := r.GetResourceResultGroup(ctx, attempt.ID())
	if err != nil {
		return nil, source, err
	}
	if group.TaskID != id.String() || group.RunID != run.ID().String() || group.AttemptID != attempt.ID().String() || group.OutcomeKind != "document" || group.Outcome != "review_ready" {
		return bad("Parent proposal result lineage is unavailable.")
	}
	_, digest, err := group.CanonicalJSON()
	if err != nil {
		return nil, source, err
	}
	if expectedDigest != "" && expectedDigest != fmt.Sprintf("%x", digest) {
		return bad("Parent proposal result digest has changed.")
	}
	report, err := r.GetAgentReportForAttempt(ctx, attempt.ID())
	if err != nil {
		return nil, source, err
	}
	if report.ID().String() != group.AgentReportID || report.RunID() != run.ID() || report.AttemptID() != attempt.ID() || report.FinalText() != group.Markdown {
		return bad("Parent proposal report does not match its result.")
	}
	events, err := r.ListRunEvents(ctx, run.ID())
	if err != nil {
		return nil, source, err
	}
	final, text, complete := domain.CurrentCompleteAssistantEvidence(events)
	if !complete || final != group.FinalAssistant || text != group.Markdown {
		return bad("Parent proposal requires the current complete, untruncated final Agent message.")
	}
	resource, err := r.GetTaskResourceSnapshot(ctx, id)
	if err != nil {
		return nil, source, err
	}
	binding, err := r.GetSpecCodingBinding(ctx, id)
	if err != nil {
		return nil, source, err
	}
	if group.ResourceSnapshotDigest != fmt.Sprintf("%x", resource.Digest) || group.ContractDigest != fmt.Sprintf("%x", binding.ActiveContractDigest) || group.ContextDigest != fmt.Sprintf("%x", binding.SnapshotDigest) {
		return bad("Parent proposal resource or contract authority has changed.")
	}
	proposal, err := domain.ParseDelegationProposal(text)
	if err != nil {
		return nil, source, err
	}
	resultID, err := domain.ParseResultID(group.ID)
	if err != nil {
		return nil, source, err
	}
	eventID, err := domain.ParseEventID(final.EventID)
	if err != nil {
		return nil, source, err
	}
	source = domain.DelegationProposalSource{ParentTaskID: id, ProjectDocumentSource: domain.ProjectDocumentSource{RunID: run.ID(), AttemptID: attempt.ID(), ResultID: resultID, AgentReportID: report.ID(), EventID: eventID, EventSequence: final.Sequence, ResultDigest: digest, SourceTextDigest: sha256.Sum256([]byte(text))}, PlanDigest: domain.DelegationPlanDigest(proposal.Assignments)}
	return proposal.Assignments, source, nil
}
