package domain

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type idValue struct {
	uuid uuid.UUID
}

func newIDValue() idValue {
	value, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("create UUIDv7: %v", err))
	}
	return idValue{uuid: value}
}

func parseIDValue(value, prefix string) (idValue, error) {
	if value == "" || !strings.HasPrefix(value, prefix) {
		return idValue{}, fmt.Errorf("%w: expected prefix %q", ErrInvalidID, prefix)
	}
	payload := strings.TrimPrefix(value, prefix)
	parsed, err := uuid.Parse(payload)
	if err != nil || parsed.String() != payload || parsed == uuid.Nil || parsed.Variant() != uuid.RFC4122 || parsed.Version() != 7 {
		return idValue{}, fmt.Errorf("%w: %q is not a UUIDv7 %s", ErrInvalidID, value, prefix)
	}
	return idValue{uuid: parsed}, nil
}

func (id idValue) valid() bool {
	return id.uuid != uuid.Nil && id.uuid.Variant() == uuid.RFC4122 && id.uuid.Version() == 7
}

func (id idValue) string(prefix string) string {
	if !id.valid() {
		return ""
	}
	return prefix + id.uuid.String()
}

type RoomID struct{ idValue }
type TaskID struct{ idValue }
type CriterionID struct{ idValue }
type RunID struct{ idValue }
type AttemptID struct{ idValue }
type EventID struct{ idValue }
type ContextEntryID struct{ idValue }
type ContextRevisionID struct{ idValue }
type CharterID struct{ idValue }
type ContextSnapshotID struct{ idValue }
type ReviewDecisionID struct{ idValue }
type ArtifactID struct{ idValue }
type ObservationID struct{ idValue }
type CheckID struct{ idValue }
type InterventionID struct{ idValue }
type HandoffID struct{ idValue }
type RuntimeSessionID struct{ idValue }
type CandidateID struct{ idValue }
type CandidateDecisionID struct{ idValue }
type DecisionGateID struct{ idValue }
type AgentReportID struct{ idValue }
type VerificationRunID struct{ idValue }
type VerificationAttemptID struct{ idValue }
type VerificationCommandEvidenceID struct{ idValue }
type ResultID struct{ idValue }
type TechnicalPlanDraftID struct{ idValue }
type TechnicalPlanRevisionID struct{ idValue }
type TechnicalPlanReviewID struct{ idValue }

func NewRoomID() RoomID                           { return RoomID{newIDValue()} }
func NewTaskID() TaskID                           { return TaskID{newIDValue()} }
func NewCriterionID() CriterionID                 { return CriterionID{newIDValue()} }
func NewRunID() RunID                             { return RunID{newIDValue()} }
func NewAttemptID() AttemptID                     { return AttemptID{newIDValue()} }
func NewEventID() EventID                         { return EventID{newIDValue()} }
func NewContextEntryID() ContextEntryID           { return ContextEntryID{newIDValue()} }
func NewContextRevisionID() ContextRevisionID     { return ContextRevisionID{newIDValue()} }
func NewCharterID() CharterID                     { return CharterID{newIDValue()} }
func NewContextSnapshotID() ContextSnapshotID     { return ContextSnapshotID{newIDValue()} }
func NewReviewDecisionID() ReviewDecisionID       { return ReviewDecisionID{newIDValue()} }
func NewArtifactID() ArtifactID                   { return ArtifactID{newIDValue()} }
func NewObservationID() ObservationID             { return ObservationID{newIDValue()} }
func NewCheckID() CheckID                         { return CheckID{newIDValue()} }
func NewInterventionID() InterventionID           { return InterventionID{newIDValue()} }
func NewHandoffID() HandoffID                     { return HandoffID{newIDValue()} }
func NewRuntimeSessionID() RuntimeSessionID       { return RuntimeSessionID{newIDValue()} }
func NewCandidateID() CandidateID                 { return CandidateID{newIDValue()} }
func NewCandidateDecisionID() CandidateDecisionID { return CandidateDecisionID{newIDValue()} }
func NewDecisionGateID() DecisionGateID           { return DecisionGateID{newIDValue()} }
func NewAgentReportID() AgentReportID             { return AgentReportID{newIDValue()} }
func NewVerificationRunID() VerificationRunID     { return VerificationRunID{newIDValue()} }
func NewVerificationAttemptID() VerificationAttemptID {
	return VerificationAttemptID{newIDValue()}
}
func NewVerificationCommandEvidenceID() VerificationCommandEvidenceID {
	return VerificationCommandEvidenceID{newIDValue()}
}
func NewResultID() ResultID { return ResultID{newIDValue()} }
func NewTechnicalPlanDraftID() TechnicalPlanDraftID {
	return TechnicalPlanDraftID{newIDValue()}
}
func NewTechnicalPlanRevisionID() TechnicalPlanRevisionID {
	return TechnicalPlanRevisionID{newIDValue()}
}
func NewTechnicalPlanReviewID() TechnicalPlanReviewID {
	return TechnicalPlanReviewID{newIDValue()}
}
func (id RoomID) String() string                { return id.idValue.string("room_") }
func (id TaskID) String() string                { return id.idValue.string("task_") }
func (id CriterionID) String() string           { return id.idValue.string("criterion_") }
func (id RunID) String() string                 { return id.idValue.string("run_") }
func (id AttemptID) String() string             { return id.idValue.string("attempt_") }
func (id EventID) String() string               { return id.idValue.string("event_") }
func (id ContextEntryID) String() string        { return id.idValue.string("context_entry_") }
func (id ContextRevisionID) String() string     { return id.idValue.string("context_revision_") }
func (id CharterID) String() string             { return id.idValue.string("charter_") }
func (id ContextSnapshotID) String() string     { return id.idValue.string("context_snapshot_") }
func (id ReviewDecisionID) String() string      { return id.idValue.string("review_decision_") }
func (id ArtifactID) String() string            { return id.idValue.string("artifact_") }
func (id ObservationID) String() string         { return id.idValue.string("observation_") }
func (id CheckID) String() string               { return id.idValue.string("check_") }
func (id InterventionID) String() string        { return id.idValue.string("intervention_") }
func (id HandoffID) String() string             { return id.idValue.string("handoff_") }
func (id RuntimeSessionID) String() string      { return id.idValue.string("runtime_session_") }
func (id CandidateID) String() string           { return id.idValue.string("candidate_") }
func (id CandidateDecisionID) String() string   { return id.idValue.string("candidate_decision_") }
func (id DecisionGateID) String() string        { return id.idValue.string("decision_gate_") }
func (id AgentReportID) String() string         { return id.idValue.string("agent_report_") }
func (id VerificationRunID) String() string     { return id.idValue.string("verification_run_") }
func (id VerificationAttemptID) String() string { return id.idValue.string("verification_attempt_") }
func (id VerificationCommandEvidenceID) String() string {
	return id.idValue.string("verification_command_evidence_")
}
func (id ResultID) String() string                   { return id.idValue.string("result_") }
func (id TechnicalPlanDraftID) String() string       { return id.idValue.string("plan_draft_") }
func (id TechnicalPlanRevisionID) String() string    { return id.idValue.string("plan_revision_") }
func (id TechnicalPlanReviewID) String() string      { return id.idValue.string("plan_review_") }
func (id RoomID) Valid() bool                        { return id.idValue.valid() }
func (id TaskID) Valid() bool                        { return id.idValue.valid() }
func (id CriterionID) Valid() bool                   { return id.idValue.valid() }
func (id RunID) Valid() bool                         { return id.idValue.valid() }
func (id AttemptID) Valid() bool                     { return id.idValue.valid() }
func (id EventID) Valid() bool                       { return id.idValue.valid() }
func (id ContextEntryID) Valid() bool                { return id.idValue.valid() }
func (id ContextRevisionID) Valid() bool             { return id.idValue.valid() }
func (id CharterID) Valid() bool                     { return id.idValue.valid() }
func (id ContextSnapshotID) Valid() bool             { return id.idValue.valid() }
func (id ReviewDecisionID) Valid() bool              { return id.idValue.valid() }
func (id ArtifactID) Valid() bool                    { return id.idValue.valid() }
func (id ObservationID) Valid() bool                 { return id.idValue.valid() }
func (id CheckID) Valid() bool                       { return id.idValue.valid() }
func (id InterventionID) Valid() bool                { return id.idValue.valid() }
func (id HandoffID) Valid() bool                     { return id.idValue.valid() }
func (id RuntimeSessionID) Valid() bool              { return id.idValue.valid() }
func (id CandidateID) Valid() bool                   { return id.idValue.valid() }
func (id CandidateDecisionID) Valid() bool           { return id.idValue.valid() }
func (id DecisionGateID) Valid() bool                { return id.idValue.valid() }
func (id AgentReportID) Valid() bool                 { return id.idValue.valid() }
func (id VerificationRunID) Valid() bool             { return id.idValue.valid() }
func (id VerificationAttemptID) Valid() bool         { return id.idValue.valid() }
func (id VerificationCommandEvidenceID) Valid() bool { return id.idValue.valid() }
func (id ResultID) Valid() bool                      { return id.idValue.valid() }
func (id TechnicalPlanDraftID) Valid() bool          { return id.idValue.valid() }
func (id TechnicalPlanRevisionID) Valid() bool       { return id.idValue.valid() }
func (id TechnicalPlanReviewID) Valid() bool         { return id.idValue.valid() }

func ParseRoomID(value string) (RoomID, error) {
	id, err := parseIDValue(value, "room_")
	return RoomID{id}, err
}
func ParseTaskID(value string) (TaskID, error) {
	id, err := parseIDValue(value, "task_")
	return TaskID{id}, err
}
func ParseCriterionID(value string) (CriterionID, error) {
	id, err := parseIDValue(value, "criterion_")
	return CriterionID{id}, err
}
func ParseRunID(value string) (RunID, error) {
	id, err := parseIDValue(value, "run_")
	return RunID{id}, err
}
func ParseAttemptID(value string) (AttemptID, error) {
	id, err := parseIDValue(value, "attempt_")
	return AttemptID{id}, err
}
func ParseEventID(value string) (EventID, error) {
	id, err := parseIDValue(value, "event_")
	return EventID{id}, err
}
func ParseContextEntryID(value string) (ContextEntryID, error) {
	id, err := parseIDValue(value, "context_entry_")
	return ContextEntryID{id}, err
}
func ParseContextRevisionID(value string) (ContextRevisionID, error) {
	id, err := parseIDValue(value, "context_revision_")
	return ContextRevisionID{id}, err
}
func ParseCharterID(value string) (CharterID, error) {
	id, err := parseIDValue(value, "charter_")
	return CharterID{id}, err
}
func ParseContextSnapshotID(value string) (ContextSnapshotID, error) {
	id, err := parseIDValue(value, "context_snapshot_")
	return ContextSnapshotID{id}, err
}
func ParseReviewDecisionID(value string) (ReviewDecisionID, error) {
	id, err := parseIDValue(value, "review_decision_")
	return ReviewDecisionID{id}, err
}
func ParseArtifactID(value string) (ArtifactID, error) {
	id, err := parseIDValue(value, "artifact_")
	return ArtifactID{id}, err
}
func ParseObservationID(value string) (ObservationID, error) {
	id, err := parseIDValue(value, "observation_")
	return ObservationID{id}, err
}
func ParseCheckID(value string) (CheckID, error) {
	id, err := parseIDValue(value, "check_")
	return CheckID{id}, err
}
func ParseInterventionID(value string) (InterventionID, error) {
	id, err := parseIDValue(value, "intervention_")
	return InterventionID{id}, err
}
func ParseHandoffID(value string) (HandoffID, error) {
	id, err := parseIDValue(value, "handoff_")
	return HandoffID{id}, err
}
func ParseRuntimeSessionID(value string) (RuntimeSessionID, error) {
	id, err := parseIDValue(value, "runtime_session_")
	return RuntimeSessionID{id}, err
}
func ParseCandidateID(value string) (CandidateID, error) {
	id, err := parseIDValue(value, "candidate_")
	return CandidateID{id}, err
}
func ParseCandidateDecisionID(value string) (CandidateDecisionID, error) {
	id, err := parseIDValue(value, "candidate_decision_")
	return CandidateDecisionID{id}, err
}
func ParseDecisionGateID(value string) (DecisionGateID, error) {
	id, err := parseIDValue(value, "decision_gate_")
	return DecisionGateID{id}, err
}
func ParseAgentReportID(value string) (AgentReportID, error) {
	id, err := parseIDValue(value, "agent_report_")
	return AgentReportID{id}, err
}
func ParseVerificationRunID(value string) (VerificationRunID, error) {
	id, err := parseIDValue(value, "verification_run_")
	return VerificationRunID{id}, err
}
func ParseVerificationAttemptID(value string) (VerificationAttemptID, error) {
	id, err := parseIDValue(value, "verification_attempt_")
	return VerificationAttemptID{id}, err
}
func ParseVerificationCommandEvidenceID(value string) (VerificationCommandEvidenceID, error) {
	id, err := parseIDValue(value, "verification_command_evidence_")
	return VerificationCommandEvidenceID{id}, err
}
func ParseResultID(value string) (ResultID, error) {
	id, err := parseIDValue(value, "result_")
	return ResultID{id}, err
}
func ParseTechnicalPlanDraftID(value string) (TechnicalPlanDraftID, error) {
	id, err := parseIDValue(value, "plan_draft_")
	return TechnicalPlanDraftID{id}, err
}
func ParseTechnicalPlanRevisionID(value string) (TechnicalPlanRevisionID, error) {
	id, err := parseIDValue(value, "plan_revision_")
	return TechnicalPlanRevisionID{id}, err
}
func ParseTechnicalPlanReviewID(value string) (TechnicalPlanReviewID, error) {
	id, err := parseIDValue(value, "plan_review_")
	return TechnicalPlanReviewID{id}, err
}
