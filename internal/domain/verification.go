package domain

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type AgentClaimedCheck struct {
	CriterionID CriterionID
	Status      string
	Evidence    string
}

type AgentReportParams struct {
	ID            AgentReportID
	RunID         RunID
	AttemptID     AttemptID
	Summary       string
	FinalText     string
	ClaimedChecks []AgentClaimedCheck
	CompletedAt   time.Time
}

type AgentReport struct {
	id            AgentReportID
	runID         RunID
	attemptID     AttemptID
	summary       string
	finalText     string
	claimedChecks []AgentClaimedCheck
	completedAt   time.Time
}

func NewAgentReport(params AgentReportParams) (AgentReport, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || !params.AttemptID.Valid() || params.CompletedAt.IsZero() {
		return AgentReport{}, fmt.Errorf("%w: invalid agent report", ErrInvalidArgument)
	}
	for _, claim := range params.ClaimedChecks {
		if !claim.CriterionID.Valid() || !validAgentClaimedStatus(claim.Status) {
			return AgentReport{}, fmt.Errorf("%w: invalid agent claimed check", ErrInvalidArgument)
		}
	}
	return AgentReport{id: params.ID, runID: params.RunID, attemptID: params.AttemptID, summary: params.Summary,
		finalText: params.FinalText, claimedChecks: cloneAgentClaimedChecks(params.ClaimedChecks), completedAt: params.CompletedAt}, nil
}

func validAgentClaimedStatus(status string) bool {
	switch status {
	case "PASS", "FAIL", "UNKNOWN", "passed", "failed", "unknown":
		return true
	default:
		return false
	}
}

func (report AgentReport) ID() AgentReportID      { return report.id }
func (report AgentReport) RunID() RunID           { return report.runID }
func (report AgentReport) AttemptID() AttemptID   { return report.attemptID }
func (report AgentReport) Summary() string        { return report.summary }
func (report AgentReport) FinalText() string      { return report.finalText }
func (report AgentReport) CompletedAt() time.Time { return report.completedAt }
func (report AgentReport) ClaimedChecks() []AgentClaimedCheck {
	return cloneAgentClaimedChecks(report.claimedChecks)
}

func cloneAgentClaimedChecks(checks []AgentClaimedCheck) []AgentClaimedCheck {
	return append([]AgentClaimedCheck(nil), checks...)
}

type VerificationBindings struct {
	BaselineDigest           [32]byte
	PatchDigest              [32]byte
	ContextSnapshotDigest    [32]byte
	AcceptanceContractDigest [32]byte
	VerifierPolicyVersion    string
	VerifierPolicyDigest     [32]byte
}

func (bindings VerificationBindings) valid() bool {
	zero := [32]byte{}
	return bindings.BaselineDigest != zero && bindings.PatchDigest != zero && bindings.ContextSnapshotDigest != zero &&
		bindings.AcceptanceContractDigest != zero && strings.TrimSpace(bindings.VerifierPolicyVersion) != "" && bindings.VerifierPolicyDigest != zero
}

type VerificationRunState string

const (
	VerificationRunAwaiting         VerificationRunState = "awaiting"
	VerificationRunVerifying        VerificationRunState = "verifying"
	VerificationRunCompleted        VerificationRunState = "completed"
	VerificationRunRecoveryRequired VerificationRunState = "recovery_required"
)

type VerificationRun struct {
	id             VerificationRunID
	runID          RunID
	agentAttemptID AttemptID
	bindings       VerificationBindings
	state          VerificationRunState
	createdAt      time.Time
	updatedAt      time.Time
}

func NewVerificationRun(id VerificationRunID, runID RunID, agentAttemptID AttemptID, bindings VerificationBindings, createdAt time.Time) (VerificationRun, error) {
	if !id.Valid() || !runID.Valid() || !agentAttemptID.Valid() || !bindings.valid() || createdAt.IsZero() {
		return VerificationRun{}, fmt.Errorf("%w: invalid verification run", ErrInvalidArgument)
	}
	return VerificationRun{id: id, runID: runID, agentAttemptID: agentAttemptID, bindings: bindings, state: VerificationRunAwaiting, createdAt: createdAt, updatedAt: createdAt}, nil
}

func (run VerificationRun) ID() VerificationRunID          { return run.id }
func (run VerificationRun) RunID() RunID                   { return run.runID }
func (run VerificationRun) AgentAttemptID() AttemptID      { return run.agentAttemptID }
func (run VerificationRun) Bindings() VerificationBindings { return run.bindings }
func (run VerificationRun) State() VerificationRunState    { return run.state }
func (run VerificationRun) CreatedAt() time.Time           { return run.createdAt }
func (run VerificationRun) UpdatedAt() time.Time           { return run.updatedAt }

type VerificationRunEvent string

const (
	VerificationRunStart              VerificationRunEvent = "start"
	VerificationRunEvidenceCompleted  VerificationRunEvent = "evidence_completed"
	VerificationRunCancelConfirmed    VerificationRunEvent = "cancel_confirmed"
	VerificationRunIntegrityUncertain VerificationRunEvent = "integrity_uncertain"
	VerificationRunRetry              VerificationRunEvent = "retry"
)

func (run VerificationRun) Transition(event VerificationRunEvent, at time.Time) (VerificationRun, error) {
	if at.Before(run.updatedAt) {
		return run, fmt.Errorf("%w: invalid verification run transition time", ErrInvalidArgument)
	}
	next := VerificationRunState("")
	switch {
	case run.state == VerificationRunAwaiting && event == VerificationRunStart:
		next = VerificationRunVerifying
	case run.state == VerificationRunVerifying && event == VerificationRunEvidenceCompleted:
		next = VerificationRunCompleted
	case run.state == VerificationRunVerifying && event == VerificationRunCancelConfirmed:
		next = VerificationRunAwaiting
	case run.state == VerificationRunVerifying && event == VerificationRunIntegrityUncertain:
		next = VerificationRunRecoveryRequired
	case (run.state == VerificationRunRecoveryRequired || run.state == VerificationRunAwaiting) && event == VerificationRunRetry:
		next = VerificationRunVerifying
	default:
		return run, fmt.Errorf("%w: invalid verification run transition", ErrInvalidArgument)
	}
	run.state = next
	run.updatedAt = at
	return run, nil
}

type VerificationRunRecord struct {
	ID             VerificationRunID
	RunID          RunID
	AgentAttemptID AttemptID
	Bindings       VerificationBindings
	State          VerificationRunState
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func RestoreVerificationRun(record VerificationRunRecord) (VerificationRun, error) {
	run, err := NewVerificationRun(record.ID, record.RunID, record.AgentAttemptID, record.Bindings, record.CreatedAt)
	if err != nil || record.UpdatedAt.Before(record.CreatedAt) {
		return VerificationRun{}, fmt.Errorf("%w: invalid persisted verification run", ErrInvalidArgument)
	}
	switch record.State {
	case VerificationRunAwaiting, VerificationRunVerifying, VerificationRunCompleted, VerificationRunRecoveryRequired:
		run.state, run.updatedAt = record.State, record.UpdatedAt
		return run, nil
	default:
		return VerificationRun{}, fmt.Errorf("%w: invalid persisted verification run state", ErrInvalidArgument)
	}
}

type VerificationAttemptState string

const (
	VerificationAttemptPending          VerificationAttemptState = "pending"
	VerificationAttemptRunning          VerificationAttemptState = "running"
	VerificationAttemptCompleted        VerificationAttemptState = "completed"
	VerificationAttemptCancelled        VerificationAttemptState = "cancelled"
	VerificationAttemptRecoveryRequired VerificationAttemptState = "recovery_required"
)

type VerificationAttemptEvent string

const (
	VerificationAttemptStart              VerificationAttemptEvent = "start"
	VerificationAttemptEvidenceCompleted  VerificationAttemptEvent = "evidence_completed"
	VerificationAttemptCancelConfirmed    VerificationAttemptEvent = "cancel_confirmed"
	VerificationAttemptIntegrityUncertain VerificationAttemptEvent = "integrity_uncertain"
)

type VerificationAttemptParams struct {
	ID                VerificationAttemptID
	VerificationRunID VerificationRunID
	Sequence          int
	Predecessor       *VerificationAttemptID
	Bindings          VerificationBindings
	CreatedAt         time.Time
}

type VerificationAttempt struct {
	id                VerificationAttemptID
	verificationRunID VerificationRunID
	sequence          int
	predecessor       VerificationAttemptID
	hasPredecessor    bool
	bindings          VerificationBindings
	state             VerificationAttemptState
	createdAt         time.Time
}

func NewVerificationAttempt(params VerificationAttemptParams) (VerificationAttempt, error) {
	if !params.ID.Valid() || !params.VerificationRunID.Valid() || params.Sequence < 1 || !params.Bindings.valid() || params.CreatedAt.IsZero() ||
		params.Sequence == 1 && params.Predecessor != nil || params.Sequence > 1 && (params.Predecessor == nil || !params.Predecessor.Valid()) {
		return VerificationAttempt{}, fmt.Errorf("%w: invalid verification attempt", ErrInvalidArgument)
	}
	attempt := VerificationAttempt{id: params.ID, verificationRunID: params.VerificationRunID, sequence: params.Sequence,
		bindings: params.Bindings, state: VerificationAttemptPending, createdAt: params.CreatedAt}
	if params.Predecessor != nil {
		attempt.predecessor, attempt.hasPredecessor = *params.Predecessor, true
	}
	return attempt, nil
}

func (attempt VerificationAttempt) Transition(event VerificationAttemptEvent) (VerificationAttempt, error) {
	next := VerificationAttemptState("")
	switch {
	case attempt.state == VerificationAttemptPending && event == VerificationAttemptStart:
		next = VerificationAttemptRunning
	case attempt.state == VerificationAttemptRunning && event == VerificationAttemptEvidenceCompleted:
		next = VerificationAttemptCompleted
	case attempt.state == VerificationAttemptRunning && event == VerificationAttemptCancelConfirmed:
		next = VerificationAttemptCancelled
	case attempt.state == VerificationAttemptRunning && event == VerificationAttemptIntegrityUncertain:
		next = VerificationAttemptRecoveryRequired
	default:
		return attempt, fmt.Errorf("%w: invalid verification attempt transition", ErrInvalidArgument)
	}
	attempt.state = next
	return attempt, nil
}

func (attempt VerificationAttempt) ID() VerificationAttemptID { return attempt.id }
func (attempt VerificationAttempt) VerificationRunID() VerificationRunID {
	return attempt.verificationRunID
}
func (attempt VerificationAttempt) Sequence() int { return attempt.sequence }
func (attempt VerificationAttempt) Predecessor() (VerificationAttemptID, bool) {
	return attempt.predecessor, attempt.hasPredecessor
}
func (attempt VerificationAttempt) Bindings() VerificationBindings  { return attempt.bindings }
func (attempt VerificationAttempt) State() VerificationAttemptState { return attempt.state }
func (attempt VerificationAttempt) CreatedAt() time.Time            { return attempt.createdAt }

type VerificationAttemptRecord struct {
	ID                VerificationAttemptID
	VerificationRunID VerificationRunID
	Sequence          int
	Predecessor       *VerificationAttemptID
	Bindings          VerificationBindings
	State             VerificationAttemptState
	CreatedAt         time.Time
}

func RestoreVerificationAttempt(record VerificationAttemptRecord) (VerificationAttempt, error) {
	attempt, err := NewVerificationAttempt(VerificationAttemptParams{ID: record.ID, VerificationRunID: record.VerificationRunID, Sequence: record.Sequence,
		Predecessor: record.Predecessor, Bindings: record.Bindings, CreatedAt: record.CreatedAt})
	if err != nil {
		return VerificationAttempt{}, err
	}
	switch record.State {
	case VerificationAttemptPending, VerificationAttemptRunning, VerificationAttemptCompleted, VerificationAttemptCancelled, VerificationAttemptRecoveryRequired:
		attempt.state = record.State
		return attempt, nil
	default:
		return VerificationAttempt{}, fmt.Errorf("%w: invalid persisted verification attempt state", ErrInvalidArgument)
	}
}

type AcceptanceCheckStatus string

const (
	AcceptanceCheckPassed  AcceptanceCheckStatus = "passed"
	AcceptanceCheckFailed  AcceptanceCheckStatus = "failed"
	AcceptanceCheckUnknown AcceptanceCheckStatus = "unknown"
)

type AcceptanceCheck struct {
	id          CheckID
	criterionID CriterionID
	status      AcceptanceCheckStatus
	trust       VerificationEvidenceTrust
	evidenceIDs []VerificationCommandEvidenceID
}

type VerificationEvidenceTrust string

const (
	VerificationEvidenceTrusted   VerificationEvidenceTrust = "trusted"
	VerificationEvidenceUntrusted VerificationEvidenceTrust = "untrusted"
)

func NewAcceptanceCheck(id CheckID, criterionID CriterionID, status AcceptanceCheckStatus, trust VerificationEvidenceTrust, evidenceIDs []VerificationCommandEvidenceID) (AcceptanceCheck, error) {
	if !id.Valid() || !criterionID.Valid() || status != AcceptanceCheckPassed && status != AcceptanceCheckFailed && status != AcceptanceCheckUnknown || trust != VerificationEvidenceTrusted || len(evidenceIDs) == 0 {
		return AcceptanceCheck{}, fmt.Errorf("%w: invalid acceptance check", ErrInvalidArgument)
	}
	seen := make(map[VerificationCommandEvidenceID]struct{}, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		if !evidenceID.Valid() {
			return AcceptanceCheck{}, fmt.Errorf("%w: invalid acceptance check evidence", ErrInvalidArgument)
		}
		if _, exists := seen[evidenceID]; exists {
			return AcceptanceCheck{}, fmt.Errorf("%w: duplicate acceptance check evidence", ErrInvalidArgument)
		}
		seen[evidenceID] = struct{}{}
	}
	return AcceptanceCheck{id: id, criterionID: criterionID, status: status, trust: trust, evidenceIDs: append([]VerificationCommandEvidenceID(nil), evidenceIDs...)}, nil
}

func (check AcceptanceCheck) ID() CheckID                      { return check.id }
func (check AcceptanceCheck) CriterionID() CriterionID         { return check.criterionID }
func (check AcceptanceCheck) Status() AcceptanceCheckStatus    { return check.status }
func (check AcceptanceCheck) Trust() VerificationEvidenceTrust { return check.trust }
func (check AcceptanceCheck) EvidenceIDs() []VerificationCommandEvidenceID {
	return append([]VerificationCommandEvidenceID(nil), check.evidenceIDs...)
}

type ResultOutcome string

const (
	ResultReviewReady   ResultOutcome = "review_ready"
	ResultNeedsRevision ResultOutcome = "needs_revision"
)

func AggregateAcceptanceChecks(checks []AcceptanceCheck) (ResultOutcome, error) {
	if len(checks) == 0 {
		return "", fmt.Errorf("%w: missing acceptance checks", ErrInvalidArgument)
	}
	seen := make(map[CriterionID]struct{}, len(checks))
	outcome := ResultReviewReady
	for _, check := range checks {
		if !check.id.Valid() || !check.criterionID.Valid() || check.trust != VerificationEvidenceTrusted || len(check.evidenceIDs) == 0 {
			return "", fmt.Errorf("%w: invalid acceptance check", ErrInvalidArgument)
		}
		if _, exists := seen[check.criterionID]; exists {
			return "", fmt.Errorf("%w: duplicate criterion check", ErrInvalidArgument)
		}
		seen[check.criterionID] = struct{}{}
		if check.status == AcceptanceCheckFailed || check.status == AcceptanceCheckUnknown {
			outcome = ResultNeedsRevision
		} else if check.status != AcceptanceCheckPassed {
			return "", fmt.Errorf("%w: invalid acceptance check status", ErrInvalidArgument)
		}
	}
	return outcome, nil
}

type VerificationCompletionProof struct {
	EvidenceComplete bool
	CleanupProven    bool
}

type VerificationResult struct {
	id                    ResultID
	verificationRunID     VerificationRunID
	verificationAttemptID VerificationAttemptID
	bindings              VerificationBindings
	checks                []AcceptanceCheck
	outcome               ResultOutcome
	createdAt             time.Time
}

func NewVerificationResult(id ResultID, run VerificationRun, attempt VerificationAttempt, checks []AcceptanceCheck, proof VerificationCompletionProof, createdAt time.Time) (VerificationResult, error) {
	if !id.Valid() || !run.id.Valid() || !attempt.id.Valid() || attempt.verificationRunID != run.id || attempt.bindings != run.bindings ||
		attempt.state != VerificationAttemptCompleted || !proof.EvidenceComplete || !proof.CleanupProven || createdAt.IsZero() || createdAt.Before(run.createdAt) {
		return VerificationResult{}, fmt.Errorf("%w: verification Result not commit-ready", ErrInvalidArgument)
	}
	outcome, err := AggregateAcceptanceChecks(checks)
	if err != nil {
		return VerificationResult{}, err
	}
	return VerificationResult{id: id, verificationRunID: run.id, verificationAttemptID: attempt.id, bindings: run.bindings,
		checks: append([]AcceptanceCheck(nil), checks...), outcome: outcome, createdAt: createdAt}, nil
}

func (result VerificationResult) ID() ResultID { return result.id }
func (result VerificationResult) VerificationRunID() VerificationRunID {
	return result.verificationRunID
}
func (result VerificationResult) VerificationAttemptID() VerificationAttemptID {
	return result.verificationAttemptID
}
func (result VerificationResult) Bindings() VerificationBindings { return result.bindings }
func (result VerificationResult) Checks() []AcceptanceCheck {
	return append([]AcceptanceCheck(nil), result.checks...)
}
func (result VerificationResult) Outcome() ResultOutcome { return result.outcome }
func (result VerificationResult) CreatedAt() time.Time   { return result.createdAt }

type VerificationCommandClassification string

const (
	VerificationCommandExited       VerificationCommandClassification = "exited"
	VerificationCommandTimedOut     VerificationCommandClassification = "timed_out"
	VerificationCommandCancelled    VerificationCommandClassification = "cancelled"
	VerificationCommandLaunchFailed VerificationCommandClassification = "launch_failed"
	VerificationCommandUnavailable  VerificationCommandClassification = "unavailable"
)

type VerificationStreamLog struct {
	FullSHA256             [32]byte
	TotalBytes             int64
	RetainedBody           string
	Truncated              bool
	TruncationBoundary     int64
	RedactionPolicyVersion string
}

func (log VerificationStreamLog) valid() bool {
	if log.FullSHA256 == ([32]byte{}) || log.TotalBytes < 0 || int64(len(log.RetainedBody)) > log.TotalBytes || strings.TrimSpace(log.RedactionPolicyVersion) == "" {
		return false
	}
	if log.Truncated {
		return log.TruncationBoundary > 0 && log.TruncationBoundary == int64(len(log.RetainedBody)) && log.TotalBytes > log.TruncationBoundary
	}
	return log.TruncationBoundary == 0
}

type VerificationCommandEvidenceParams struct {
	ID                    VerificationCommandEvidenceID
	VerificationAttemptID VerificationAttemptID
	CommandID             string
	CriterionIDs          []CriterionID
	Argv                  []string
	StartedAt             time.Time
	EndedAt               time.Time
	Classification        VerificationCommandClassification
	ExitCode              *int
	Stdout                VerificationStreamLog
	Stderr                VerificationStreamLog
	VerifierIdentity      string
	WorkspaceIdentity     string
}

type VerificationCommandEvidence struct {
	id                    VerificationCommandEvidenceID
	verificationAttemptID VerificationAttemptID
	commandID             string
	criterionIDs          []CriterionID
	argv                  []string
	startedAt             time.Time
	endedAt               time.Time
	classification        VerificationCommandClassification
	exitCode              *int
	stdout                VerificationStreamLog
	stderr                VerificationStreamLog
	verifierIdentity      string
	workspaceIdentity     string
}

func validVerificationIdentity(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func NewVerificationCommandEvidence(params VerificationCommandEvidenceParams) (VerificationCommandEvidence, error) {
	if !params.ID.Valid() || !params.VerificationAttemptID.Valid() || strings.TrimSpace(params.CommandID) == "" || len(params.CriterionIDs) == 0 ||
		len(params.Argv) == 0 || params.StartedAt.IsZero() || params.EndedAt.Before(params.StartedAt) || !params.Stdout.valid() || !params.Stderr.valid() ||
		strings.TrimSpace(params.VerifierIdentity) == "" || !validVerificationIdentity(params.WorkspaceIdentity) {
		return VerificationCommandEvidence{}, fmt.Errorf("%w: invalid verification command evidence", ErrInvalidArgument)
	}
	for _, arg := range params.Argv {
		if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, '\x00') {
			return VerificationCommandEvidence{}, fmt.Errorf("%w: invalid verification argv", ErrInvalidArgument)
		}
	}
	seen := make(map[CriterionID]struct{}, len(params.CriterionIDs))
	for _, criterionID := range params.CriterionIDs {
		if !criterionID.Valid() {
			return VerificationCommandEvidence{}, fmt.Errorf("%w: invalid verification criterion binding", ErrInvalidArgument)
		}
		if _, exists := seen[criterionID]; exists {
			return VerificationCommandEvidence{}, fmt.Errorf("%w: duplicate verification criterion binding", ErrInvalidArgument)
		}
		seen[criterionID] = struct{}{}
	}
	switch params.Classification {
	case VerificationCommandExited:
		if params.ExitCode == nil {
			return VerificationCommandEvidence{}, fmt.Errorf("%w: exited verification command missing exit code", ErrInvalidArgument)
		}
	case VerificationCommandTimedOut, VerificationCommandCancelled, VerificationCommandLaunchFailed, VerificationCommandUnavailable:
		if params.ExitCode != nil {
			return VerificationCommandEvidence{}, fmt.Errorf("%w: non-exit verification command has exit code", ErrInvalidArgument)
		}
	default:
		return VerificationCommandEvidence{}, fmt.Errorf("%w: invalid verification command classification", ErrInvalidArgument)
	}
	var exitCode *int
	if params.ExitCode != nil {
		value := *params.ExitCode
		exitCode = &value
	}
	return VerificationCommandEvidence{id: params.ID, verificationAttemptID: params.VerificationAttemptID, commandID: params.CommandID,
		criterionIDs: append([]CriterionID(nil), params.CriterionIDs...), argv: append([]string(nil), params.Argv...),
		startedAt: params.StartedAt, endedAt: params.EndedAt, classification: params.Classification, exitCode: exitCode,
		stdout: params.Stdout, stderr: params.Stderr, verifierIdentity: params.VerifierIdentity, workspaceIdentity: params.WorkspaceIdentity}, nil
}

func (evidence VerificationCommandEvidence) ID() VerificationCommandEvidenceID { return evidence.id }
func (evidence VerificationCommandEvidence) VerificationAttemptID() VerificationAttemptID {
	return evidence.verificationAttemptID
}
func (evidence VerificationCommandEvidence) CommandID() string { return evidence.commandID }
func (evidence VerificationCommandEvidence) CriterionIDs() []CriterionID {
	return append([]CriterionID(nil), evidence.criterionIDs...)
}
func (evidence VerificationCommandEvidence) Argv() []string {
	return append([]string(nil), evidence.argv...)
}
func (evidence VerificationCommandEvidence) StartedAt() time.Time { return evidence.startedAt }
func (evidence VerificationCommandEvidence) EndedAt() time.Time   { return evidence.endedAt }
func (evidence VerificationCommandEvidence) Classification() VerificationCommandClassification {
	return evidence.classification
}
func (evidence VerificationCommandEvidence) ExitCode() (int, bool) {
	if evidence.exitCode == nil {
		return 0, false
	}
	return *evidence.exitCode, true
}
func (evidence VerificationCommandEvidence) Stdout() VerificationStreamLog { return evidence.stdout }
func (evidence VerificationCommandEvidence) Stderr() VerificationStreamLog { return evidence.stderr }
func (evidence VerificationCommandEvidence) VerifierIdentity() string {
	return evidence.verifierIdentity
}
func (evidence VerificationCommandEvidence) WorkspaceIdentity() string {
	return evidence.workspaceIdentity
}
