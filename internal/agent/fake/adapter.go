package fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/execution"
)

type Adapter struct {
	mu               sync.Mutex
	plan             Plan
	startRequest     *execution.StartRequest
	decisionOptionID string
}

func NewAdapter(plan Plan) *Adapter {
	plan.Arguments = append([]string(nil), plan.Arguments...)
	environment := make(map[string]string, len(plan.Environment))
	for key, value := range plan.Environment {
		environment[key] = value
	}
	plan.Environment = environment
	plan.TerminalResult = append([]byte(nil), plan.TerminalResult...)
	if plan.DecisionGate != nil {
		gate := *plan.DecisionGate
		gate.Options = append([]DecisionOption(nil), gate.Options...)
		plan.DecisionGate = &gate
	}
	if plan.ResumeMode == "" {
		plan.ResumeMode = execution.ResumeUnsupported
	}
	if plan.RuntimeFingerprint.Digest == ([32]byte{}) {
		plan.RuntimeFingerprint.Digest = sha256.Sum256([]byte(plan.AdapterID + "\x00" + plan.Executable))
	}
	if plan.RuntimeFingerprint.Version == "" {
		plan.RuntimeFingerprint.Version = "fake-v1"
	}
	return &Adapter{plan: plan}
}

func (adapter *Adapter) ID() string { return adapter.plan.AdapterID }
func (adapter *Adapter) Capabilities() execution.AdapterCapabilities {
	return execution.AdapterCapabilities{ResumeMode: adapter.plan.ResumeMode}
}

func (adapter *Adapter) PrepareStart(_ context.Context, request execution.StartRequest) (execution.Invocation, error) {
	return adapter.prepareStart(request, execution.ExecutionTarget{})
}

// PrepareBoundStart preserves the immutable profile/provider binding even when
// the build-tagged E2E surface substitutes deterministic Fake execution for the
// registered adapter. Production routing still decides whether this Adapter is
// present; Fake never selects or weakens a profile itself.
func (adapter *Adapter) PrepareBoundStart(_ context.Context, request execution.StartRequest) (execution.StartPreparation, error) {
	binding := request.Attempt.AgentExecutionProfileBinding()
	if !binding.Bound() || request.Attempt.AdapterID() != adapter.plan.AdapterID {
		return execution.StartPreparation{}, errors.New("fake bound start requires an exact matching Attempt binding")
	}
	target, err := execution.NewExecutionTarget(adapter.plan.AdapterID, binding.ExecutionProvider())
	if err != nil {
		return execution.StartPreparation{}, err
	}
	invocation, err := adapter.prepareStart(request, target)
	if err != nil {
		return execution.StartPreparation{}, err
	}
	return execution.NewStartPreparation(invocation, adapter.plan.RuntimeFingerprint)
}

func (adapter *Adapter) prepareStart(request execution.StartRequest, target execution.ExecutionTarget) (execution.Invocation, error) {
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: adapter.plan.AdapterID, Executable: adapter.plan.Executable,
		Arguments: adapter.plan.Arguments, Environment: adapter.plan.Environment,
		WorkingRoot: request.WorkspaceRoot, Stdin: request.SnapshotDocument,
		LaunchToken: request.LaunchToken, Target: target,
	})
	if err != nil {
		return execution.Invocation{}, err
	}
	adapter.mu.Lock()
	stored := request
	stored.SnapshotDocument = append([]byte(nil), request.SnapshotDocument...)
	adapter.startRequest = &stored
	adapter.decisionOptionID = ""
	adapter.mu.Unlock()
	return invocation, nil
}

func (adapter *Adapter) PrepareResume(_ context.Context, request execution.ResumeRequest) (execution.Invocation, error) {
	if err := agent.ValidateExplicitResume(adapter.Capabilities(), request, adapter.plan.RuntimeFingerprint); err != nil {
		return execution.Invocation{}, err
	}
	arguments := append(append([]string(nil), adapter.plan.Arguments...), "--resume", request.Binding.ExternalSession)
	return execution.NewInvocation(execution.InvocationParams{AdapterID: adapter.plan.AdapterID, Executable: adapter.plan.Executable, Arguments: arguments, Environment: adapter.plan.Environment, WorkingRoot: request.Binding.WorkingRoot, Stdin: request.DeltaInstruction, LaunchToken: request.LaunchToken})
}

func (adapter *Adapter) DecodeEvent(chunk execution.EventChunk) (execution.DecodedChunk, error) {
	consumed := len(chunk.Data)
	if !chunk.EOF {
		last := bytes.LastIndexByte(chunk.Data, '\n')
		if last < 0 {
			return execution.DecodedChunk{}, nil
		}
		consumed = last + 1
	}
	data := chunk.Data[:consumed]
	result := execution.DecodedChunk{ConsumedBytes: consumed}
	base := chunk.Offset
	for len(data) > 0 {
		line := data
		advance := len(data)
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			line = data[:index]
			advance = index + 1
		}
		if len(bytes.TrimSpace(line)) > 0 {
			result.Events = append(result.Events, decodeLine(chunk.Stream, base, line))
		}
		base += int64(advance)
		data = data[advance:]
	}
	return result, nil
}

func decodeLine(stream execution.StreamKind, offset int64, line []byte) execution.NormalizedEvent {
	var document struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(line, &document); err != nil || document.Type == "" {
		normalized, _ := json.Marshal(map[string]any{"type": "adapter.parse_error", "stream": stream, "offset": offset})
		return execution.NormalizedEvent{Type: "adapter.parse_error", NormalizedJSON: normalized, RawJSON: json.RawMessage(`{"redacted":"parse_error"}`)}
	}
	raw := append(json.RawMessage(nil), line...)
	return execution.NormalizedEvent{Type: document.Type, NormalizedJSON: raw, RawJSON: raw, ExternalSession: document.ThreadID}
}

func (adapter *Adapter) DecodeTerminal(execution.TerminalFiles) (execution.TerminalResult, error) {
	adapter.mu.Lock()
	var request *execution.StartRequest
	if adapter.startRequest != nil {
		value := *adapter.startRequest
		value.SnapshotDocument = append([]byte(nil), adapter.startRequest.SnapshotDocument...)
		request = &value
	}
	decision := adapter.decisionOptionID
	adapter.mu.Unlock()
	if adapter.plan.SnapshotEvidence {
		if request == nil {
			return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "snapshot_evidence_missing"}, nil
		}
		return snapshotEvidenceResult(*request)
	}
	result, err := agent.DecodeResult(adapter.plan.TerminalResult)
	if err != nil {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}, nil
	}
	if adapter.plan.DecisionGate != nil && decision != "continue" {
		result.Kind = execution.TerminalRevisionRequired
		result.Unknowns = append(result.Unknowns, "The execution Decision Gate did not authorize continuation.")
	}
	return result, nil
}

func (adapter *Adapter) DecisionGatePlan() (DecisionGatePlan, bool) {
	if adapter.plan.DecisionGate == nil {
		return DecisionGatePlan{}, false
	}
	value := *adapter.plan.DecisionGate
	value.Options = append([]DecisionOption(nil), value.Options...)
	return value, true
}

func (adapter *Adapter) ResolveDecision(optionID string) error {
	gate, ok := adapter.DecisionGatePlan()
	if !ok {
		return errors.New("fake adapter has no Decision Gate")
	}
	for _, option := range gate.Options {
		if option.ID == optionID {
			adapter.mu.Lock()
			adapter.decisionOptionID = optionID
			adapter.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("unknown fake Decision Gate option %q", optionID)
}

func snapshotEvidenceResult(request execution.StartRequest) (execution.TerminalResult, error) {
	var document struct {
		AcceptanceCriteria []struct {
			ID string `json:"id"`
		} `json:"acceptance_criteria"`
		Decisions []struct {
			RevisionID string `json:"revision_id"`
			Title      string `json:"title"`
			Body       string `json:"body"`
		} `json:"decisions"`
		Selection struct {
			Selected []struct {
				RevisionID string `json:"revision_id"`
				Provenance struct {
					Kind string `json:"kind"`
				} `json:"provenance"`
			} `json:"selected"`
			Excluded []struct {
				RevisionID string `json:"revision_id"`
			} `json:"excluded"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(request.SnapshotDocument, &document); err != nil {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "snapshot_evidence_invalid"}, nil
	}
	var candidateID, title, body string
	included := make([]string, 0, len(document.Selection.Selected))
	for _, item := range document.Selection.Selected {
		included = append(included, item.RevisionID)
		if item.Provenance.Kind == "accepted_run_candidate" {
			candidateID = item.RevisionID
		}
	}
	for _, revision := range document.Decisions {
		if revision.RevisionID == candidateID {
			title, body = revision.Title, revision.Body
			break
		}
	}
	excluded := make([]string, 0, len(document.Selection.Excluded))
	for _, item := range document.Selection.Excluded {
		excluded = append(excluded, item.RevisionID)
	}
	if candidateID == "" || title == "" || body == "" {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "snapshot_candidate_missing"}, nil
	}
	checks := make([]execution.DeclaredCheck, 0, len(document.AcceptanceCriteria))
	for _, criterion := range document.AcceptanceCriteria {
		checks = append(checks, execution.DeclaredCheck{CriterionID: criterion.ID, Status: execution.CheckPass, Evidence: "Production Fake consumed the immutable Context Snapshot."})
	}
	return execution.TerminalResult{
		Kind: execution.TerminalReviewReady, Summary: "Production Fake consumed the selected confirmed Candidate Revision and omitted excluded content.",
		Outputs: []execution.WorkspaceArtifact{{Locator: "snapshot-consumption.json", Description: "Deterministic frozen Context consumption evidence"}}, Checks: checks,
		ContextConsumption: &execution.ContextConsumption{
			SnapshotID: request.Attempt.ContextSnapshotID().String(), SnapshotDigest: fmt.Sprintf("%x", request.Attempt.ContextDigest()), CandidateRevisionID: candidateID,
			IncludedRevisionIDs: included, ExcludedRevisionIDs: excluded, Derivation: execution.DeriveConfirmedContext(candidateID, title, body), ExcludedContentObserved: false,
		},
	}, nil
}

func (adapter *Adapter) Fingerprint(context.Context) (execution.RuntimeFingerprint, error) {
	return adapter.plan.RuntimeFingerprint, nil
}

var _ execution.AgentAdapter = (*Adapter)(nil)
var _ execution.BoundStartPreparer = (*Adapter)(nil)
