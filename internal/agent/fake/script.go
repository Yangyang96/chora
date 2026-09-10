package fake

import (
	"crypto/sha256"
	"fmt"

	"github.com/Yangyang96/chora/internal/execution"
)

type Plan struct {
	AdapterID          string
	Executable         string
	Arguments          []string
	Environment        map[string]string
	ResumeMode         execution.ResumeMode
	RuntimeFingerprint execution.RuntimeFingerprint
	TerminalResult     []byte
	DecisionGate       *DecisionGatePlan
	SnapshotEvidence   bool
}

type DecisionGatePlan struct {
	Question       string
	Context        string
	Options        []DecisionOption
	Recommendation string
	Impact         string
}

type DecisionOption struct {
	ID     string
	Label  string
	Impact string
}

type StreamFact struct {
	Stream execution.StreamKind
	Chunk  execution.StreamChunk
}

type SupervisorFacts struct {
	StartOutcome      execution.StartOutcome
	ReadChunks        []StreamFact
	DrainOutcomes     []execution.DrainOutcome
	StopOutcome       execution.StopOutcome
	StopBlocks        bool
	ReconcileOutcomes []execution.ReconcileOutcome
}

type Script struct {
	Name       string
	Plan       Plan
	Supervisor SupervisorFacts
}

func NewScript(name, criterionID string) (Script, error) {
	if name == "" || criterionID == "" {
		return Script{}, fmt.Errorf("fake script name and criterion are required")
	}
	stdout := []byte(`{"type":"thread.started","thread_id":"session-scripted"}` + "\n")
	stderr := []byte(`{"type":"runtime.notice","level":"info"}` + "\n")
	stdoutOffset := int64(len(stdout))
	stderrOffset := int64(len(stderr))
	plan := Plan{AdapterID: "fake", Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, RuntimeFingerprint: execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte("fake-runtime-v1")), Version: "fake-v1"}}
	alive := execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: execution.RuntimeHandle{Value: "handle-scripted"}}
	facts := SupervisorFacts{StartOutcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle-scripted"}, Identity: execution.ProcessIdentity{Value: "pid:scripted"}}, ReadChunks: []StreamFact{{Stream: execution.StreamStdout, Chunk: execution.StreamChunk{Data: stdout, NextOffset: stdoutOffset}}, {Stream: execution.StreamStderr, Chunk: execution.StreamChunk{Data: stderr, NextOffset: stderrOffset}}}, ReconcileOutcomes: []execution.ReconcileOutcome{alive, alive}}
	eofDrain := execution.DrainOutcome{Chunks: map[execution.StreamKind][]byte{}, Offsets: execution.StreamOffsets{execution.StreamStdout: stdoutOffset, execution.StreamStderr: stderrOffset}, EOF: map[execution.StreamKind]bool{execution.StreamStdout: true, execution.StreamStderr: true}, TerminalFiles: execution.TerminalFiles{ExitCode: 0}}
	switch name {
	case ScenarioSuccess:
		plan.TerminalResult = []byte(fmt.Sprintf(`{"schema_version":"chora.agent-result.v1","summary":"done","review_ready":true,"outputs":[{"locator":"report.md","description":"report"}],"artifact_candidates":[],"checks":[{"criterion_id":%q,"status":"PASS","evidence":"ok"}],"unknowns":[],"handoff":{"requested":false,"reason":""}}`, criterionID))
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioNeedsRevision:
		plan.TerminalResult = []byte(`{"schema_version":"chora.agent-result.v1","summary":"revise","review_ready":false,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":["missing"],"handoff":{"requested":false,"reason":""}}`)
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioHandoff:
		plan.TerminalResult = []byte(`{"schema_version":"chora.agent-result.v1","summary":"ask","review_ready":false,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":true,"reason":"owner decision"}}`)
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioMalformedResult:
		plan.TerminalResult = []byte(`{malformed`)
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioNonzeroExit:
		plan.TerminalResult = []byte(`{"schema_version":"chora.agent-result.v1","summary":"ignored","review_ready":true,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":false,"reason":""}}`)
		eofDrain.TerminalFiles.ExitCode = 7
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioSlowCancellation:
		facts.StopBlocks = true
		facts.StopOutcome = execution.StopOutcome{Kind: execution.StopConfirmed}
		facts.ReconcileOutcomes = append(facts.ReconcileOutcomes, alive)
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	case ScenarioUncertainStop:
		facts.StopOutcome = execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: "scripted uncertain stop"}
		facts.ReconcileOutcomes = append(facts.ReconcileOutcomes, alive)
	case ScenarioRestartRecovery:
		facts.ReconcileOutcomes = append(facts.ReconcileOutcomes, execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle-scripted"}})
		facts.DrainOutcomes = []execution.DrainOutcome{eofDrain}
	default:
		return Script{}, fmt.Errorf("unknown fake scenario %q", name)
	}
	script := Script{Name: name, Plan: plan, Supervisor: facts}
	if err := script.Validate(); err != nil {
		return Script{}, err
	}
	return script, nil
}

func (script Script) Validate() error {
	if script.Name == "" || script.Plan.AdapterID == "" || script.Plan.Executable == "" || !script.Plan.RuntimeFingerprint.Valid() || script.Supervisor.StartOutcome.Kind != execution.Started || !script.Supervisor.StartOutcome.Identity.Valid() || len(script.Supervisor.ReadChunks) == 0 {
		return fmt.Errorf("incomplete fake script")
	}
	seenStreams := map[execution.StreamKind]bool{}
	for _, fact := range script.Supervisor.ReadChunks {
		if fact.Stream != execution.StreamStdout && fact.Stream != execution.StreamStderr || len(fact.Chunk.Data) == 0 || fact.Chunk.NextOffset != int64(len(fact.Chunk.Data)) {
			return fmt.Errorf("invalid stream fact")
		}
		seenStreams[fact.Stream] = true
	}
	if !seenStreams[execution.StreamStdout] || !seenStreams[execution.StreamStderr] {
		return fmt.Errorf("both stream transcripts are required")
	}
	completeDrain := func() bool {
		if len(script.Supervisor.DrainOutcomes) == 0 {
			return false
		}
		last := script.Supervisor.DrainOutcomes[len(script.Supervisor.DrainOutcomes)-1]
		return last.EOF[execution.StreamStdout] && last.EOF[execution.StreamStderr]
	}
	switch script.Name {
	case ScenarioSuccess, ScenarioNeedsRevision, ScenarioHandoff, ScenarioMalformedResult:
		if len(script.Plan.TerminalResult) == 0 || !completeDrain() {
			return fmt.Errorf("missing terminal facts")
		}
	case ScenarioNonzeroExit:
		if !completeDrain() || script.Supervisor.DrainOutcomes[len(script.Supervisor.DrainOutcomes)-1].TerminalFiles.ExitCode == 0 {
			return fmt.Errorf("missing nonzero exit")
		}
	case ScenarioSlowCancellation:
		if !script.Supervisor.StopBlocks || script.Supervisor.StopOutcome.Kind != execution.StopConfirmed || !completeDrain() {
			return fmt.Errorf("missing slow cancellation facts")
		}
	case ScenarioUncertainStop:
		if script.Supervisor.StopOutcome.Kind != execution.StopUncertain || len(script.Supervisor.ReconcileOutcomes) == 0 {
			return fmt.Errorf("missing uncertain stop facts")
		}
	case ScenarioRestartRecovery:
		if len(script.Supervisor.ReconcileOutcomes) == 0 || script.Supervisor.ReconcileOutcomes[len(script.Supervisor.ReconcileOutcomes)-1].Kind != execution.ReconcileDead || !completeDrain() {
			return fmt.Errorf("missing restart recovery facts")
		}
	default:
		return fmt.Errorf("unknown fake scenario %q", script.Name)
	}
	return nil
}

const (
	ScenarioSuccess          = "success"
	ScenarioNeedsRevision    = "needs_revision"
	ScenarioHandoff          = "handoff"
	ScenarioMalformedResult  = "malformed_result"
	ScenarioNonzeroExit      = "nonzero_exit"
	ScenarioSlowCancellation = "slow_cancellation"
	ScenarioUncertainStop    = "uncertain_stop"
	ScenarioRestartRecovery  = "restart_recovery"
)
