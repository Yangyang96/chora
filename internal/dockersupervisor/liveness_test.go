package dockersupervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

type livenessRunner struct {
	CommandRunner
	result CommandResult
	err    error
	calls  int
}

func (r *livenessRunner) Run(_ context.Context, c Command) (CommandResult, error) {
	r.calls++
	return r.result, r.err
}

func TestWorkbenchReconcileObservesContainerDespiteLiveCLI(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		err         error
		want        execution.ReconcileOutcomeKind
	}{
		{"running", `{"Running":true,"Paused":false,"Restarting":false,"Dead":false}`, nil, execution.ReconcileAlive},
		{"exited", `{"Running":false,"Paused":false,"Restarting":false,"Dead":false}`, nil, execution.ReconcileUncertain},
		{"daemon unavailable", "", errors.New("daemon unavailable"), execution.ReconcileUncertain},
		{"invalid observation", `{}`, nil, execution.ReconcileUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &livenessRunner{result: CommandResult{Stdout: []byte(tc.state)}, err: tc.err}
			record := &attemptRecord{identity: execution.ProcessIdentity{Value: "identity"}, handle: execution.RuntimeHandle{Value: "handle"}, containerDockerID: "container-id", startReturned: true}
			s := &Supervisor{config: Config{Runner: runner, Workbench: &WorkbenchConfig{}}, byIdentity: map[string]*attemptRecord{"identity": record}}
			got, err := s.Reconcile(context.Background(), record.identity)
			if err != nil || got.Kind != tc.want {
				t.Fatalf("Reconcile=%+v err=%v want=%s", got, err, tc.want)
			}
			if runner.calls != 1 {
				t.Fatalf("container was not observed: calls=%d", runner.calls)
			}
			if record.isExited() {
				t.Fatal("container observation fabricated trusted process-tree death")
			}
		})
	}
}
