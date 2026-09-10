package fake

import (
	"bytes"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestNamedScriptsProvideCompleteConsumableFacts(t *testing.T) {
	criterion := "criterion_1"
	for _, name := range []string{ScenarioSuccess, ScenarioNeedsRevision, ScenarioHandoff, ScenarioMalformedResult, ScenarioNonzeroExit, ScenarioSlowCancellation, ScenarioUncertainStop, ScenarioRestartRecovery} {
		t.Run(name, func(t *testing.T) {
			script, err := NewScript(name, criterion)
			if err != nil {
				t.Fatal(err)
			}
			if script.Name != name || script.Plan.AdapterID == "" || script.Plan.Executable == "" || !script.Plan.RuntimeFingerprint.Valid() {
				t.Fatalf("incomplete plan: %#v", script)
			}
			if script.Supervisor.StartOutcome.Kind == "" || len(script.Supervisor.ReadChunks) == 0 {
				t.Fatalf("missing start/transcript facts: %#v", script.Supervisor)
			}
			for _, fact := range script.Supervisor.ReadChunks {
				if len(fact.Chunk.Data) == 0 || !bytes.HasSuffix(fact.Chunk.Data, []byte("\n")) || fact.Chunk.NextOffset <= 0 {
					t.Fatalf("invalid transcript chunk: %#v", fact)
				}
			}
			switch name {
			case ScenarioSuccess, ScenarioNeedsRevision, ScenarioHandoff, ScenarioMalformedResult:
				if len(script.Plan.TerminalResult) == 0 || len(script.Supervisor.DrainOutcomes) == 0 {
					t.Fatalf("missing terminal facts: %#v", script)
				}
			case ScenarioNonzeroExit:
				if len(script.Supervisor.DrainOutcomes) == 0 || script.Supervisor.DrainOutcomes[len(script.Supervisor.DrainOutcomes)-1].TerminalFiles.ExitCode == 0 {
					t.Fatalf("missing nonzero exit facts: %#v", script.Supervisor)
				}
			case ScenarioSlowCancellation:
				if !script.Supervisor.StopBlocks || script.Supervisor.StopOutcome.Kind != execution.StopConfirmed || len(script.Supervisor.DrainOutcomes) == 0 {
					t.Fatalf("missing slow cancellation facts: %#v", script.Supervisor)
				}
			case ScenarioUncertainStop:
				if script.Supervisor.StopOutcome.Kind != execution.StopUncertain {
					t.Fatalf("missing uncertain stop: %#v", script.Supervisor)
				}
			case ScenarioRestartRecovery:
				if len(script.Supervisor.ReconcileOutcomes) == 0 || script.Supervisor.ReconcileOutcomes[len(script.Supervisor.ReconcileOutcomes)-1].Kind != execution.ReconcileDead || len(script.Supervisor.DrainOutcomes) == 0 {
					t.Fatalf("missing recovery facts: %#v", script.Supervisor)
				}
			}
			if err := script.Validate(); err != nil {
				t.Fatalf("script invalid: %v", err)
			}
		})
	}
}

func TestScriptValidationRejectsIncompleteFacts(t *testing.T) {
	if _, err := NewScript("implicit-last-session", "criterion_1"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
	if err := (Script{Name: ScenarioSuccess}).Validate(); err == nil {
		t.Fatal("empty success script accepted")
	}
	base, err := NewScript(ScenarioSuccess, "criterion_1")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Script){
		"missing stderr transcript": func(s *Script) { s.Supervisor.ReadChunks = s.Supervisor.ReadChunks[:1] },
		"missing start identity":    func(s *Script) { s.Supervisor.StartOutcome.Identity = execution.ProcessIdentity{} },
		"undrained stdout":          func(s *Script) { s.Supervisor.DrainOutcomes[0].EOF[execution.StreamStdout] = false },
		"missing terminal result":   func(s *Script) { s.Plan.TerminalResult = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			script := base
			script.Supervisor.ReadChunks = append([]StreamFact(nil), base.Supervisor.ReadChunks...)
			script.Supervisor.DrainOutcomes = append([]execution.DrainOutcome(nil), base.Supervisor.DrainOutcomes...)
			if len(script.Supervisor.DrainOutcomes) > 0 {
				eof := map[execution.StreamKind]bool{}
				for key, value := range script.Supervisor.DrainOutcomes[0].EOF {
					eof[key] = value
				}
				script.Supervisor.DrainOutcomes[0].EOF = eof
			}
			mutate(&script)
			if err := script.Validate(); err == nil {
				t.Fatal("incomplete script accepted")
			}
		})
	}
}
