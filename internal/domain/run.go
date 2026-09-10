package domain

import (
	"fmt"
	"strings"
	"time"
)

type CapabilityEnvelope map[string]bool

type RunCharterParams struct {
	ID                            CharterID
	TaskID                        TaskID
	TaskGoal                      string
	Criteria                      []AcceptanceCriterion
	ContextRevisionIDs            []ContextRevisionID
	ConfirmedSensitiveRevisionIDs []ContextRevisionID
	WorkspaceRoot                 string
	AdapterID                     string
	SandboxMode                   string
	SensitiveExclusions           []ContextEntryID
	ExpectedOutput                string
	ResponsibleHuman              string
	CapabilityEnvelope            CapabilityEnvelope
	AgentExecutionProfileBinding  AgentExecutionProfileBinding
	Initiator                     string
	CreatedAt                     time.Time
}

type RunCharter struct {
	id                            CharterID
	taskID                        TaskID
	taskGoal                      string
	criteria                      []AcceptanceCriterion
	contextRevisionIDs            []ContextRevisionID
	confirmedSensitiveRevisionIDs []ContextRevisionID
	workspaceRoot                 string
	adapterID                     string
	sandboxMode                   string
	sensitiveExclusions           []ContextEntryID
	expectedOutput                string
	responsibleHuman              string
	capabilityEnvelope            CapabilityEnvelope
	agentExecutionProfileBinding  AgentExecutionProfileBinding
	initiator                     string
	createdAt                     time.Time
}

func NewRunCharter(params RunCharterParams) (RunCharter, error) {
	if !params.ID.Valid() || !params.TaskID.Valid() || strings.TrimSpace(params.TaskGoal) == "" || len(params.Criteria) == 0 || !validCriteria(params.Criteria) || len(params.ContextRevisionIDs) == 0 || !allContextRevisionIDsValid(params.ContextRevisionIDs) || !allContextRevisionIDsValid(params.ConfirmedSensitiveRevisionIDs) || !canonicalAbsolutePath(params.WorkspaceRoot) || strings.TrimSpace(params.AdapterID) == "" || strings.TrimSpace(params.SandboxMode) == "" || !allContextEntryIDsValid(params.SensitiveExclusions) || strings.TrimSpace(params.ExpectedOutput) == "" || strings.TrimSpace(params.ResponsibleHuman) == "" || len(params.CapabilityEnvelope) == 0 || !validAgentExecutionProfileBinding(params.AdapterID, params.AgentExecutionProfileBinding) || strings.TrimSpace(params.Initiator) == "" || params.CreatedAt.IsZero() {
		return RunCharter{}, fmt.Errorf("%w: invalid run charter", ErrInvalidArgument)
	}
	return RunCharter{
		id: params.ID, taskID: params.TaskID, taskGoal: params.TaskGoal, criteria: cloneCriteria(params.Criteria),
		contextRevisionIDs: append([]ContextRevisionID(nil), params.ContextRevisionIDs...), workspaceRoot: params.WorkspaceRoot,
		confirmedSensitiveRevisionIDs: uniqueContextRevisionIDs(params.ConfirmedSensitiveRevisionIDs),
		adapterID:                     params.AdapterID, sandboxMode: params.SandboxMode, sensitiveExclusions: append([]ContextEntryID(nil), params.SensitiveExclusions...),
		expectedOutput: params.ExpectedOutput, responsibleHuman: params.ResponsibleHuman, capabilityEnvelope: cloneCapabilities(params.CapabilityEnvelope), agentExecutionProfileBinding: params.AgentExecutionProfileBinding,
		initiator: params.Initiator, createdAt: params.CreatedAt,
	}, nil
}

func (charter RunCharter) ID() CharterID                   { return charter.id }
func (charter RunCharter) TaskID() TaskID                  { return charter.taskID }
func (charter RunCharter) TaskGoal() string                { return charter.taskGoal }
func (charter RunCharter) Criteria() []AcceptanceCriterion { return cloneCriteria(charter.criteria) }
func (charter RunCharter) ContextRevisionIDs() []ContextRevisionID {
	return append([]ContextRevisionID(nil), charter.contextRevisionIDs...)
}
func (charter RunCharter) ConfirmedSensitiveRevisionIDs() []ContextRevisionID {
	return append([]ContextRevisionID(nil), charter.confirmedSensitiveRevisionIDs...)
}
func (charter RunCharter) WorkspaceRoot() string { return charter.workspaceRoot }
func (charter RunCharter) AdapterID() string     { return charter.adapterID }
func (charter RunCharter) SandboxMode() string   { return charter.sandboxMode }
func (charter RunCharter) SensitiveExclusions() []ContextEntryID {
	return append([]ContextEntryID(nil), charter.sensitiveExclusions...)
}
func (charter RunCharter) ExpectedOutput() string   { return charter.expectedOutput }
func (charter RunCharter) ResponsibleHuman() string { return charter.responsibleHuman }
func (charter RunCharter) CapabilityEnvelope() CapabilityEnvelope {
	return cloneCapabilities(charter.capabilityEnvelope)
}
func (charter RunCharter) AgentExecutionProfileBinding() AgentExecutionProfileBinding {
	return charter.agentExecutionProfileBinding
}
func (charter RunCharter) Initiator() string    { return charter.initiator }
func (charter RunCharter) CreatedAt() time.Time { return charter.createdAt }

func cloneCapabilities(capabilities CapabilityEnvelope) CapabilityEnvelope {
	cloned := make(CapabilityEnvelope, len(capabilities))
	for capability, allowed := range capabilities {
		cloned[capability] = allowed
	}
	return cloned
}

type AgentRun struct {
	id                   RunID
	taskID               TaskID
	charterID            CharterID
	state                RunState
	version              uint64
	currentAttemptNumber int
	createdAt            time.Time
	updatedAt            time.Time
	startedAt            time.Time
	reviewRequestedAt    time.Time
	terminalAt           time.Time
}

func NewAgentRun(id RunID, taskID TaskID, charterID CharterID, createdAt time.Time) (AgentRun, error) {
	if !id.Valid() || !taskID.Valid() || !charterID.Valid() || createdAt.IsZero() {
		return AgentRun{}, fmt.Errorf("%w: invalid agent run", ErrInvalidArgument)
	}
	return AgentRun{id: id, taskID: taskID, charterID: charterID, state: RunStateDraft, createdAt: createdAt, updatedAt: createdAt}, nil
}

func (run AgentRun) Transition(command CommandKind, at time.Time) (AgentRun, error) {
	if command == CommandReviewAccept || command == CommandReviewReject {
		return run, fmt.Errorf("%w: review commands require a ReviewDecision", ErrInvalidRunTransition)
	}
	if at.IsZero() || at.Before(run.updatedAt) {
		return run, fmt.Errorf("%w: invalid transition timestamp", ErrInvalidArgument)
	}
	previousState := run.state
	next, err := TransitionRun(run.state, command)
	if err != nil {
		return run, err
	}
	run.state = next
	run.version++
	run.updatedAt = at
	switch command {
	case CommandPrepareRun, CommandPrepareRetry:
		run.currentAttemptNumber++
		if command == CommandPrepareRetry && previousState == RunStateCancelled {
			run.terminalAt = time.Time{}
		}
	case CommandStartAttempt:
		if run.startedAt.IsZero() {
			run.startedAt = at
		}
	case CommandVerificationPassed, CommandSubmitAgentReportDirectReview:
		run.reviewRequestedAt = at
	case CommandStopConfirmedForCancel, CommandCancelInactiveRun, CommandCompleteNoChange:
		run.terminalAt = at
	}
	return run, nil
}

func (run AgentRun) ApplyReview(decision ReviewDecision) (AgentRun, error) {
	if decision.runID != run.id || decision.expectedRunVersion != run.version || decision.decidedAt.IsZero() || decision.decidedAt.Before(run.updatedAt) {
		return run, fmt.Errorf("%w: review decision does not match run", ErrInvalidArgument)
	}
	if run.state != RunStateAwaitingReview {
		return run, fmt.Errorf("%w: state %q cannot apply review", ErrInvalidRunTransition, run.state)
	}
	var command CommandKind
	switch decision.kind {
	case ReviewDecisionAccept:
		command = CommandReviewAccept
	case ReviewDecisionReject:
		command = CommandReviewReject
	default:
		return run, fmt.Errorf("%w: unknown review decision kind %q", ErrInvalidArgument, decision.kind)
	}
	next, err := TransitionRun(run.state, command)
	if err != nil {
		return run, err
	}
	run.state = next
	run.version++
	run.updatedAt = decision.decidedAt
	if decision.kind == ReviewDecisionAccept {
		run.terminalAt = decision.decidedAt
	}
	return run, nil
}

func (run AgentRun) ApplyVerifiedReview(decision VerifiedReviewDecision) (AgentRun, error) {
	if run.state != RunStateAwaitingReview {
		return run, fmt.Errorf("%w: state %q cannot apply verified review", ErrInvalidRunTransition, run.state)
	}
	if decision.runID != run.id || decision.expectedRunVersion != run.version || decision.decidedAt.IsZero() || decision.decidedAt.Before(run.updatedAt) {
		return run, fmt.Errorf("%w: verified review decision does not match run", ErrInvalidArgument)
	}
	var command CommandKind
	switch decision.kind {
	case ReviewDecisionAccept:
		command = CommandReviewAccept
	case ReviewDecisionReject:
		command = CommandReviewReject
	default:
		return run, fmt.Errorf("%w: unknown verified review decision kind %q", ErrInvalidArgument, decision.kind)
	}
	next, err := TransitionRun(run.state, command)
	if err != nil {
		return run, err
	}
	run.state = next
	run.version++
	run.updatedAt = decision.decidedAt
	if decision.kind == ReviewDecisionAccept || (decision.kind == ReviewDecisionReject && decision.rejectionClass != ReviewRejectionImplementationGap) {
		run.terminalAt = decision.decidedAt
	}
	return run, nil
}

func (run AgentRun) ID() RunID                    { return run.id }
func (run AgentRun) TaskID() TaskID               { return run.taskID }
func (run AgentRun) CharterID() CharterID         { return run.charterID }
func (run AgentRun) State() RunState              { return run.state }
func (run AgentRun) Version() uint64              { return run.version }
func (run AgentRun) CurrentAttemptNumber() int    { return run.currentAttemptNumber }
func (run AgentRun) CreatedAt() time.Time         { return run.createdAt }
func (run AgentRun) UpdatedAt() time.Time         { return run.updatedAt }
func (run AgentRun) StartedAt() time.Time         { return run.startedAt }
func (run AgentRun) ReviewRequestedAt() time.Time { return run.reviewRequestedAt }
func (run AgentRun) TerminalAt() time.Time        { return run.terminalAt }

type AttemptState string

const (
	AttemptStateCreated         AttemptState = "created"
	AttemptStateStarting        AttemptState = "starting"
	AttemptStateRunning         AttemptState = "running"
	AttemptStateOutputSubmitted AttemptState = "output_submitted"
	AttemptStateFailed          AttemptState = "failed"
	AttemptStateInterrupted     AttemptState = "interrupted"
	AttemptStateCancelled       AttemptState = "cancelled"
)

type AttemptParams struct {
	ID                           AttemptID
	RunID                        RunID
	Sequence                     int
	Predecessor                  *AttemptID
	ContextSnapshotID            ContextSnapshotID
	ContextDigest                [32]byte
	AdapterID                    string
	AgentExecutionProfileBinding AgentExecutionProfileBinding
	ExternalSession              string
	RetryReason                  string
	InterventionReason           string
	ContextDelta                 string
	CreatedAt                    time.Time
}

type Attempt struct {
	id                           AttemptID
	runID                        RunID
	sequence                     int
	predecessor                  AttemptID
	hasPredecessor               bool
	contextSnapshotID            ContextSnapshotID
	contextDigest                [32]byte
	adapterID                    string
	agentExecutionProfileBinding AgentExecutionProfileBinding
	externalSession              string
	retryReason                  string
	interventionReason           string
	contextDelta                 string
	state                        AttemptState
	createdAt                    time.Time
}

func NewAttempt(params AttemptParams) (Attempt, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || params.Sequence <= 0 || !params.ContextSnapshotID.Valid() || params.ContextDigest == ([32]byte{}) || strings.TrimSpace(params.AdapterID) == "" || !validAgentExecutionProfileBinding(params.AdapterID, params.AgentExecutionProfileBinding) || params.CreatedAt.IsZero() {
		return Attempt{}, fmt.Errorf("%w: invalid attempt", ErrInvalidArgument)
	}
	if params.Sequence == 1 && params.Predecessor != nil || params.Sequence > 1 && (params.Predecessor == nil || !params.Predecessor.Valid()) {
		return Attempt{}, fmt.Errorf("%w: invalid attempt predecessor", ErrInvalidArgument)
	}
	attempt := Attempt{id: params.ID, runID: params.RunID, sequence: params.Sequence, contextSnapshotID: params.ContextSnapshotID, contextDigest: params.ContextDigest, adapterID: params.AdapterID, agentExecutionProfileBinding: params.AgentExecutionProfileBinding, externalSession: params.ExternalSession, retryReason: params.RetryReason, interventionReason: params.InterventionReason, contextDelta: params.ContextDelta, state: AttemptStateCreated, createdAt: params.CreatedAt}
	if params.Predecessor != nil {
		attempt.predecessor = *params.Predecessor
		attempt.hasPredecessor = true
	}
	return attempt, nil
}

func (attempt Attempt) ID() AttemptID { return attempt.id }
func (attempt Attempt) RunID() RunID  { return attempt.runID }
func (attempt Attempt) Sequence() int { return attempt.sequence }
func (attempt Attempt) Predecessor() (AttemptID, bool) {
	return attempt.predecessor, attempt.hasPredecessor
}
func (attempt Attempt) ContextSnapshotID() ContextSnapshotID { return attempt.contextSnapshotID }
func (attempt Attempt) ContextDigest() [32]byte              { return attempt.contextDigest }
func (attempt Attempt) AdapterID() string                    { return attempt.adapterID }
func (attempt Attempt) AgentExecutionProfileBinding() AgentExecutionProfileBinding {
	return attempt.agentExecutionProfileBinding
}
func (attempt Attempt) ExternalSession() string    { return attempt.externalSession }
func (attempt Attempt) RetryReason() string        { return attempt.retryReason }
func (attempt Attempt) InterventionReason() string { return attempt.interventionReason }
func (attempt Attempt) ContextDelta() string       { return attempt.contextDelta }
func (attempt Attempt) State() AttemptState        { return attempt.state }
func (attempt Attempt) CreatedAt() time.Time       { return attempt.createdAt }

func (attempt Attempt) Transition(event AttemptEventKind) (Attempt, error) {
	next, err := TransitionAttempt(attempt.state, event)
	if err != nil {
		return attempt, err
	}
	attempt.state = next
	return attempt, nil
}

type ReviewDecisionKind string

const (
	ReviewDecisionAccept ReviewDecisionKind = "accept"
	ReviewDecisionReject ReviewDecisionKind = "reject"
)

type ReviewedCheck struct{ Name, Status, Evidence string }
type ArtifactLink string

// HumanReviewAuthorization is an opaque capability. The domain intentionally
// exposes no minting path until the trusted human-command and Presence flows
// are implemented.
type HumanReviewAuthorization struct {
	humanIssued        bool
	deviceOwnerPresent bool
	runID              RunID
	decisionKind       ReviewDecisionKind
	expectedRunVersion uint64
}

// MintHumanReviewAuthorization is the trusted bridge used by an application
// PresenceAuthorizer. Callers must already have verified the bound human
// session and device-owner presence.
func MintHumanReviewAuthorization(runID RunID, kind ReviewDecisionKind, expectedVersion uint64, deviceOwnerPresent bool) (HumanReviewAuthorization, error) {
	if !runID.Valid() || expectedVersion == 0 || kind != ReviewDecisionAccept && kind != ReviewDecisionReject {
		return HumanReviewAuthorization{}, fmt.Errorf("%w: invalid review authorization", ErrInvalidArgument)
	}
	return HumanReviewAuthorization{humanIssued: true, deviceOwnerPresent: deviceOwnerPresent, runID: runID, decisionKind: kind, expectedRunVersion: expectedVersion}, nil
}

type ReviewDecisionParams struct {
	ID                 ReviewDecisionID
	RunID              RunID
	ExpectedRunVersion uint64
	Comment            string
	Checks             []ReviewedCheck
	LinkedArtifacts    []ArtifactLink
	DecidedAt          time.Time
}

type ReviewDecision struct {
	id                 ReviewDecisionID
	runID              RunID
	kind               ReviewDecisionKind
	expectedRunVersion uint64
	comment            string
	checks             []ReviewedCheck
	linkedArtifacts    []ArtifactLink
	decidedAt          time.Time
}

func NewAcceptedReviewDecision(params ReviewDecisionParams, authorization HumanReviewAuthorization) (ReviewDecision, error) {
	if !authorization.matches(params, ReviewDecisionAccept) || !authorization.deviceOwnerPresent {
		return ReviewDecision{}, ErrUnauthorizedReview
	}
	return newReviewDecision(params, ReviewDecisionAccept)
}

func NewRejectedReviewDecision(params ReviewDecisionParams, authorization HumanReviewAuthorization) (ReviewDecision, error) {
	if !authorization.matches(params, ReviewDecisionReject) {
		return ReviewDecision{}, ErrUnauthorizedReview
	}
	if strings.TrimSpace(params.Comment) == "" {
		return ReviewDecision{}, fmt.Errorf("%w: rejection requires comment", ErrInvalidArgument)
	}
	return newReviewDecision(params, ReviewDecisionReject)
}

func newReviewDecision(params ReviewDecisionParams, kind ReviewDecisionKind) (ReviewDecision, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || params.ExpectedRunVersion == 0 || params.DecidedAt.IsZero() {
		return ReviewDecision{}, fmt.Errorf("%w: invalid review decision", ErrInvalidArgument)
	}
	return ReviewDecision{id: params.ID, runID: params.RunID, kind: kind, expectedRunVersion: params.ExpectedRunVersion, comment: params.Comment, checks: append([]ReviewedCheck(nil), params.Checks...), linkedArtifacts: append([]ArtifactLink(nil), params.LinkedArtifacts...), decidedAt: params.DecidedAt}, nil
}

func (authorization HumanReviewAuthorization) matches(params ReviewDecisionParams, kind ReviewDecisionKind) bool {
	return authorization.humanIssued && authorization.runID == params.RunID && authorization.decisionKind == kind && authorization.expectedRunVersion == params.ExpectedRunVersion
}

func (decision ReviewDecision) ID() ReviewDecisionID       { return decision.id }
func (decision ReviewDecision) RunID() RunID               { return decision.runID }
func (decision ReviewDecision) Kind() ReviewDecisionKind   { return decision.kind }
func (decision ReviewDecision) ExpectedRunVersion() uint64 { return decision.expectedRunVersion }
func (decision ReviewDecision) Comment() string            { return decision.comment }
func (decision ReviewDecision) Checks() []ReviewedCheck {
	return append([]ReviewedCheck(nil), decision.checks...)
}
func (decision ReviewDecision) LinkedArtifacts() []ArtifactLink {
	return append([]ArtifactLink(nil), decision.linkedArtifacts...)
}
func (decision ReviewDecision) DecidedAt() time.Time { return decision.decidedAt }
