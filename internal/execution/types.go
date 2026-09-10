package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

const FrozenSnapshotPromptPrefix = "Chora execution policy: the current directory is the repository root. Modify only the writable files and run only the exact shell commands declared in the frozen Snapshot. Do not prepend, append, or combine shell commands. Do not run Git, listing, search, or discovery commands. Never read credential, authentication, environment, trust, or unrelated host files. Never disclose secrets, personal information, vendor metadata, non-Chora content, or enterprise-project code. Vendor exists only for the offline compiler and must not be inspected. Complete the bounded change and keep existing tests passing. If a declared test fails, read only Chora repository files named by the failing test output when needed to understand compatibility constraints, repair only within the writable-file boundary, and rerun the exact declared commands until they pass or no compliant repair remains. End the Agent response only after that loop reaches one of those outcomes.\n\nFrozen Context Snapshot:\n"

const FrozenExecutionPromptPrefix = "Chora execution policy: the current directory is the repository root. Use the frozen Context Snapshot only as task context. Modify only the exact writable files. Run only declared direct-argv commands, a gofmt -w subset containing only declared writable files, or focused go test package paths covered by a declared go test ./... command. Do not add flags or prepend, append, redirect, or combine shell commands. Do not run Git, listing, search, or discovery commands. Never read credential, authentication, environment, trust, or unrelated host files. Never disclose secrets, personal information, vendor metadata, non-Chora content, or enterprise-project code. Vendor exists only for the offline compiler and must not be inspected. Complete the bounded change and keep existing tests passing. If a declared test fails, read only Chora repository files named by the failing test output when needed to understand compatibility constraints, repair only within the writable-file boundary, and rerun the exact declared commands until they pass or no compliant repair remains. End the Agent response only after that loop reaches one of those outcomes.\n\nFrozen Execution Input:\n"

const LocalConnectedExecutionPromptPrefix = "Chora Local Connected execution policy: this Task uses a separate Git worktree without a Sandbox. The current directory is the repository root. Use the frozen Context Snapshot as context. Modify, create, delete or rename ordinary text files only within exact writable_files or bounded writable_directories in the active contract. Never stage, commit, push or alter Git configuration. Do not access credentials or unrelated host files. Ignored dependencies/build output are not reviewable changes; do not copy them from the original checkout. Run the provided declared_commands exactly, in order; they include each command's working directory. Never silently install dependencies or run undeclared setup commands. If setup or a check fails, report that failure and a recovery action truthfully. If no_checks is true, do not run checks or claim verification. Successful work may have no file changes: provide a completed summary and truthful checks; do not create an artificial patch. Pi's native permissions apply. Chora validates scope and collects the immutable review Patch after completion; this is not a Sandbox guarantee.\n\nFrozen Execution Input:\n"

const TaskResourcesExecutionPromptPrefix = "Chora Local Connected task policy: the current directory is a Task workspace, with one Git worktree per selected repo_id child directory in task.resources. This is No Sandbox; Pi native permissions apply. Work only in selected repository worktrees, respect role reference (no modifications), scope and protected directories. Never alter original checkouts, stage, commit, push, access credentials or change host/Git configuration. Do not copy ignored dependencies or credentials. Inspect relevant code and committed configuration as needed; no whole-repository enumeration is required. For auto check policy, choose relevant existing checks, report what was selected and why; use explicit cd into the repo_id/directory before each command so evidence is attributable. Run each check as the sole tool call in its turn; parallel tool batches cannot establish check-content evidence. Execute each selected check as one separate ordinary command with explicit cd repo_id/directory prefix; do not append output redirection, echo, exit-code printing, pipes, semicolons, or additional shell steps. Pi reports tool success even when numeric exit code is unavailable, which is acceptable; never add a shell wrapper to fabricate or expose an exit code. For named checks run the specified commands in their directories. For none do not run checks or claim verification. Environment preparation is separate, only explicitly authorized preparation may run; explain missing dependencies and proposed setup through native permissions. Report failures, missing checks, unknown cwd and stale checks honestly; rerun checks after changing tested contents. Finish with an explicit complete summary of each repository. For every auto-check repository, include a final fenced block tagged chora-check-selection containing JSON: {\"repositories\":[{\"repoId\":\"repo_...\",\"checks\":[{\"name\":\"Relevant tests\",\"command\":\"npm test\",\"workingDirectory\":\".\"}],\"noApplicableChecks\":false,\"explanation\":\"Why these checks apply\"}]}. List selected checks, including failures or checks not run; do not claim the block proves execution. Use actual ordinary command text and repository-relative workingDirectory. If no checks apply, give checks:[], noApplicableChecks:true and a concrete explanation; missing dependencies or failed checks do not mean no applicable checks. All-empty completed work needs no artificial patch. Chora collects grouped immutable ordinary-text changes after completion and requires human Apply.\n\nFrozen Execution Input:\n"

const FrozenExecutionInputSchemaV1 = "chora.execution-input.v1"

type frozenExecutionInput struct {
	DeclaredCommands []string        `json:"declared_commands,omitempty"`
	SchemaVersion    string          `json:"schema_version"`
	ContextSnapshot  json.RawMessage `json:"context_snapshot"`
	ActiveContract   json.RawMessage `json:"active_contract"`
}

func WrapFrozenExecutionPrompt(snapshot, contract []byte) (string, error) {
	if len(bytes.TrimSpace(snapshot)) == 0 || !json.Valid(snapshot) || len(bytes.TrimSpace(contract)) == 0 || !json.Valid(contract) {
		return "", errors.New("frozen execution input requires valid Snapshot and Contract JSON")
	}
	encoded, err := json.Marshal(frozenExecutionInput{
		SchemaVersion: FrozenExecutionInputSchemaV1, ContextSnapshot: append(json.RawMessage(nil), snapshot...), ActiveContract: append(json.RawMessage(nil), contract...),
	})
	if err != nil {
		return "", fmt.Errorf("encode frozen execution input: %w", err)
	}
	return FrozenExecutionPromptPrefix + string(encoded), nil
}

func WrapLocalConnectedExecutionPrompt(snapshot, contract []byte, commands []string) (string, error) {
	if !json.Valid(snapshot) || !json.Valid(contract) {
		return "", errors.New("invalid frozen Local Connected input")
	}
	encoded, err := json.Marshal(frozenExecutionInput{SchemaVersion: FrozenExecutionInputSchemaV1, ContextSnapshot: snapshot, ActiveContract: contract, DeclaredCommands: commands})
	if err != nil {
		return "", err
	}
	var identity struct {
		SchemaVersion string `json:"schema_version"`
	}
	_ = json.Unmarshal(contract, &identity)
	if identity.SchemaVersion == "chora.spec-coding-core.v12" {
		return TaskResourcesExecutionPromptPrefix + string(encoded), nil
	}
	return LocalConnectedExecutionPromptPrefix + string(encoded), nil
}

func UnwrapFrozenExecutionPrompt(message string) (snapshot, contract []byte, ok bool) {
	prefix := FrozenExecutionPromptPrefix
	if strings.HasPrefix(message, TaskResourcesExecutionPromptPrefix) {
		prefix = TaskResourcesExecutionPromptPrefix
	}
	if strings.HasPrefix(message, LocalConnectedExecutionPromptPrefix) {
		prefix = LocalConnectedExecutionPromptPrefix
	}
	if !strings.HasPrefix(message, prefix) {
		return nil, nil, false
	}
	var input frozenExecutionInput
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(message, prefix)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.SchemaVersion != FrozenExecutionInputSchemaV1 ||
		len(bytes.TrimSpace(input.ContextSnapshot)) == 0 || !json.Valid(input.ContextSnapshot) ||
		len(bytes.TrimSpace(input.ActiveContract)) == 0 || !json.Valid(input.ActiveContract) {
		return nil, nil, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, nil, false
	}
	return append([]byte(nil), input.ContextSnapshot...), append([]byte(nil), input.ActiveContract...), true
}

func WrapFrozenSnapshotPrompt(snapshot []byte) string {
	return FrozenSnapshotPromptPrefix + string(snapshot)
}

func UnwrapFrozenSnapshotPrompt(message string) ([]byte, bool) {
	if !strings.HasPrefix(message, FrozenSnapshotPromptPrefix) {
		return nil, false
	}
	return []byte(strings.TrimPrefix(message, FrozenSnapshotPromptPrefix)), true
}

// ErrEventStreamInvalid identifies malformed provider output, not an infrastructure failure.
var ErrEventStreamInvalid = errors.New("agent event stream invalid")

var ErrResultContractInvalid = errors.New("agent result contract invalid")

type StreamKind string

const (
	StreamStdout StreamKind = "stdout"
	StreamStderr StreamKind = "stderr"
)

type StartRequest struct {
	Run                       domain.AgentRun
	Attempt                   domain.Attempt
	SnapshotDocument          []byte
	ExecutionContractDocument []byte
	WorkspaceRoot             string
	LaunchToken               LaunchToken
}

type ResumeMode string

const (
	ResumeUnsupported     ResumeMode = "unsupported"
	ResumeExplicitSession ResumeMode = "explicit_session"
)

type AdapterCapabilities struct {
	ResumeMode ResumeMode
}

type ResumeBinding struct {
	ExternalSession       string
	SessionOwnerAttemptID domain.AttemptID
	RuntimeFingerprint    RuntimeFingerprint
	WorkingRoot           string
	SecurityFingerprint   [32]byte
}

type ResumeRequest struct {
	ExecutionContractDocument []byte
	Run                       domain.AgentRun
	Attempt                   domain.Attempt
	Binding                   ResumeBinding
	SecurityFingerprint       [32]byte
	DeltaInstruction          []byte
	LaunchToken               LaunchToken
}

type InvocationParams struct {
	AdapterID, Executable, WorkingRoot string
	Arguments                          []string
	Environment                        map[string]string
	Stdin                              []byte
	LaunchToken                        LaunchToken
	Target                             ExecutionTarget
}

type Invocation struct {
	adapterID, executable, workingRoot string
	arguments                          []string
	environment                        map[string]string
	stdin                              []byte
	launchToken                        LaunchToken
	target                             ExecutionTarget
}

func NewInvocation(params InvocationParams) (Invocation, error) {
	target := params.Target
	if target == (ExecutionTarget{}) {
		var err error
		target, err = NewExecutionTarget(params.AdapterID, params.AdapterID)
		if err != nil {
			return Invocation{}, fmt.Errorf("invalid invocation: %w", err)
		}
	}
	if !target.Valid() || target.AdapterID() != params.AdapterID || strings.TrimSpace(params.Executable) == "" || strings.TrimSpace(params.WorkingRoot) == "" || !params.LaunchToken.Valid() {
		return Invocation{}, fmt.Errorf("invalid invocation")
	}
	return Invocation{
		adapterID: params.AdapterID, executable: params.Executable, workingRoot: params.WorkingRoot,
		arguments: append([]string(nil), params.Arguments...), environment: cloneEnvironment(params.Environment),
		stdin: append([]byte(nil), params.Stdin...), launchToken: params.LaunchToken, target: target,
	}, nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	clone := make(map[string]string, len(environment))
	for key, value := range environment {
		clone[key] = value
	}
	return clone
}

func (invocation Invocation) AdapterID() string  { return invocation.adapterID }
func (invocation Invocation) Executable() string { return invocation.executable }
func (invocation Invocation) Arguments() []string {
	return append([]string(nil), invocation.arguments...)
}
func (invocation Invocation) Environment() map[string]string {
	return cloneEnvironment(invocation.environment)
}
func (invocation Invocation) WorkingRoot() string      { return invocation.workingRoot }
func (invocation Invocation) Stdin() []byte            { return append([]byte(nil), invocation.stdin...) }
func (invocation Invocation) LaunchToken() LaunchToken { return invocation.launchToken }
func (invocation Invocation) Target() ExecutionTarget  { return invocation.target }

func (invocation Invocation) Valid() bool {
	return invocation.target.Valid() && invocation.target.AdapterID() == invocation.adapterID &&
		strings.TrimSpace(invocation.executable) != "" && strings.TrimSpace(invocation.workingRoot) != "" && invocation.launchToken.Valid()
}

type RuntimeFingerprint struct {
	Digest  [32]byte
	Version string
}

func (fingerprint RuntimeFingerprint) Valid() bool {
	return fingerprint.Digest != ([32]byte{}) && strings.TrimSpace(fingerprint.Version) != ""
}

type RuntimeHandle struct{ Value string }

func (h RuntimeHandle) Valid() bool { return h.Value != "" }

type ProcessIdentity struct{ Value string }

func (p ProcessIdentity) Valid() bool { return p.Value != "" }

type LaunchToken struct{ Value string }

func (token LaunchToken) Valid() bool { return token.Value != "" }

type RuntimeSink interface {
	Binding() LaunchToken
	Notify(StreamKind, int64)
	Exited()
}

type StartOutcomeKind string

const (
	Started                     StartOutcomeKind = "started"
	StartProvenNoChild          StartOutcomeKind = "proven_no_child"
	StartReconciliationRequired StartOutcomeKind = "reconciliation_required"
)

type StartOutcome struct {
	Kind        StartOutcomeKind
	Handle      RuntimeHandle
	Identity    ProcessIdentity
	LaunchToken LaunchToken
	Diagnostic  string
}

type StopIntentKind string

const (
	StopForRevision StopIntentKind = "revision"
	StopForHandoff  StopIntentKind = "handoff"
	StopForCancel   StopIntentKind = "cancel"
)

type StopIntent struct {
	Kind   StopIntentKind
	Reason string
}
type StopOutcomeKind string

const (
	StopConfirmed StopOutcomeKind = "confirmed_dead"
	StopUncertain StopOutcomeKind = "uncertain"
)

type StopOutcome struct {
	Kind       StopOutcomeKind
	Diagnostic string
}

type ReconcileOutcomeKind string

const (
	ReconcileAlive     ReconcileOutcomeKind = "alive"
	ReconcileDead      ReconcileOutcomeKind = "dead"
	ReconcileUncertain ReconcileOutcomeKind = "uncertain"
)

type ReconcileOutcome struct {
	Kind        ReconcileOutcomeKind
	Handle      RuntimeHandle
	LaunchToken LaunchToken
	Diagnostic  string
}
type StreamChunk struct {
	Data       []byte
	NextOffset int64
	EOF        bool
}

type EventChunk struct {
	// WorkingRoot is trusted session context, never a provider-supplied path.
	WorkingRoot string
	Stream      StreamKind
	Offset      int64
	Data        []byte
	EOF         bool
}

type DecodedChunk struct {
	Events        []NormalizedEvent
	ConsumedBytes int
}
type StreamOffsets map[StreamKind]int64
type DrainOutcome struct {
	Chunks        map[StreamKind][]byte
	Offsets       StreamOffsets
	EOF           map[StreamKind]bool
	TerminalFiles TerminalFiles
}
type RetentionPolicy struct{ KeepTerminal bool }

// TerminationCause is supervisor-owned terminal authority. Adapter output and
// process diagnostics cannot set or override it.
type TerminationCause string

const (
	TerminationOutputLimit      TerminationCause = "output_limit_exceeded"
	TerminationNone             TerminationCause = "none"
	TerminationExitNonzero      TerminationCause = "exit_nonzero"
	TerminationDeadlineExceeded TerminationCause = "deadline_exceeded"
)

func (cause TerminationCause) Valid() bool {
	switch cause {
	case TerminationNone, TerminationExitNonzero, TerminationDeadlineExceeded, TerminationOutputLimit:
		return true
	default:
		return false
	}
}

type TerminalFiles struct {
	Paths            map[string]string
	ExitCode         int
	TerminationCause TerminationCause
}
type NormalizedEvent struct {
	Type            string
	OccurredAt      time.Time
	NormalizedJSON  json.RawMessage
	RawJSON         json.RawMessage
	ExternalSession string
	// DiagnosticJSON is untrusted adapter diagnostic material. The application
	// never persists it in the primary operational store.
	DiagnosticJSON json.RawMessage
}
type TerminalResultKind string

const (
	TerminalReviewReady       TerminalResultKind = "review_ready"
	TerminalCompletedNoChange TerminalResultKind = "completed_no_change"
	TerminalChecksFailed      TerminalResultKind = "checks_failed"
	TerminalChecksIncomplete  TerminalResultKind = "checks_incomplete"
	TerminalRevisionRequired  TerminalResultKind = "revision_required"
	TerminalFailed            TerminalResultKind = "failed"
	TerminalHandoffRequested  TerminalResultKind = "handoff_requested"
)

type TerminalResult struct {
	Kind               TerminalResultKind  `json:"kind"`
	FailureReason      string              `json:"failure_reason,omitempty"`
	Summary            string              `json:"summary,omitempty"`
	Outputs            []WorkspaceArtifact `json:"outputs,omitempty"`
	ArtifactCandidates []WorkspaceArtifact `json:"artifact_candidates,omitempty"`
	Checks             []DeclaredCheck     `json:"checks,omitempty"`
	Unknowns           []string            `json:"unknowns,omitempty"`
	Handoff            *HandoffRequest     `json:"handoff,omitempty"`
	ContextConsumption *ContextConsumption `json:"context_consumption,omitempty"`
}

func (result TerminalResult) Valid() bool {
	if result.ContextConsumption != nil && !result.ContextConsumption.Valid() {
		return false
	}
	switch result.Kind {
	case TerminalChecksFailed, TerminalChecksIncomplete:
		return result.FailureReason == string(result.Kind) && result.Handoff == nil && strings.TrimSpace(result.Summary) != "" && len(result.Outputs) == 0
	case TerminalCompletedNoChange:
		return result.FailureReason == "" && result.Handoff == nil && strings.TrimSpace(result.Summary) != "" && len(result.Outputs) == 0
	case TerminalReviewReady, TerminalRevisionRequired:
		return result.FailureReason == "" && result.Handoff == nil
	case TerminalHandoffRequested:
		return result.FailureReason == "" && result.Handoff != nil && result.Handoff.Reason != ""
	case TerminalFailed:
		return result.FailureReason != "" && result.Summary == "" && len(result.Outputs) == 0 && len(result.ArtifactCandidates) == 0 && len(result.Checks) == 0 && len(result.Unknowns) == 0 && result.Handoff == nil && result.ContextConsumption == nil
	default:
		return false
	}
}

type ContextConsumption struct {
	SnapshotID              string   `json:"snapshot_id"`
	SnapshotDigest          string   `json:"snapshot_digest"`
	CandidateRevisionID     string   `json:"candidate_revision_id"`
	IncludedRevisionIDs     []string `json:"included_revision_ids"`
	ExcludedRevisionIDs     []string `json:"excluded_revision_ids"`
	Derivation              string   `json:"derivation"`
	ExcludedContentObserved bool     `json:"excluded_content_observed"`
}

func (evidence ContextConsumption) Valid() bool {
	if _, err := domain.ParseContextSnapshotID(evidence.SnapshotID); err != nil {
		return false
	}
	if len(evidence.SnapshotDigest) != 64 {
		return false
	}
	for _, value := range evidence.SnapshotDigest {
		if value < '0' || value > '9' && value < 'a' || value > 'f' {
			return false
		}
	}
	candidateID, err := domain.ParseContextRevisionID(evidence.CandidateRevisionID)
	if err != nil || evidence.ExcludedContentObserved || strings.TrimSpace(evidence.Derivation) == "" {
		return false
	}
	seen := make(map[domain.ContextRevisionID]bool, len(evidence.IncludedRevisionIDs)+len(evidence.ExcludedRevisionIDs))
	candidateIncluded := false
	for _, value := range evidence.IncludedRevisionIDs {
		id, err := domain.ParseContextRevisionID(value)
		if err != nil || seen[id] {
			return false
		}
		seen[id] = true
		candidateIncluded = candidateIncluded || id == candidateID
	}
	for _, value := range evidence.ExcludedRevisionIDs {
		id, err := domain.ParseContextRevisionID(value)
		if err != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return candidateIncluded
}

func DeriveConfirmedContext(revisionID, title, body string) string {
	digest := sha256.Sum256([]byte(revisionID + "\x00" + title + "\x00" + body))
	return fmt.Sprintf("sha256:%x", digest)
}

type WorkspaceArtifact struct {
	Locator     string `json:"locator"`
	Description string `json:"description"`
	SHA256      string `json:"sha256,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
}

type CheckStatus string

const (
	CheckPass    CheckStatus = "PASS"
	CheckFail    CheckStatus = "FAIL"
	CheckUnknown CheckStatus = "UNKNOWN"
)

type DeclaredCheck struct {
	CriterionID string      `json:"criterion_id"`
	Status      CheckStatus `json:"status"`
	Evidence    string      `json:"evidence"`
}

type HandoffRequest struct {
	Reason string `json:"reason"`
}
