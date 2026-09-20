package localweb

import (
	"context"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestFakeNamespacesPreserveConcurrentTargetOwnership(t *testing.T) {
	diagnostic, pi := newFakeSupervisor(), newFakeSupervisor()
	pi.identityNamespace = "e2e-pi-"
	diagnosticTarget := mustExecutionTarget(t, "fake", "fake")
	piTarget := mustExecutionTarget(t, "pi", "docker")
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{diagnosticTarget: diagnostic, piTarget: pi})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	outcomes := make([]execution.StartOutcome, 0, 2)
	for _, target := range []execution.ExecutionTarget{diagnosticTarget, piTarget} {
		launch := execution.LaunchToken{Value: "launch-" + target.AdapterID()}
		outcome := router.Start(ctx, mustRoutingInvocation(t, target, launch), routingTestSink{launch: launch})
		if outcome.Kind != execution.Started {
			t.Fatalf("%s start = %#v", target.AdapterID(), outcome)
		}
		outcomes = append(outcomes, outcome)
	}
	if outcomes[0].Handle.Value != "local-fake-handle" || outcomes[0].Identity.Value != "local-fake-process" {
		t.Fatal("default diagnostic identity changed")
	}
	if outcomes[0].Handle == outcomes[1].Handle || outcomes[0].Identity == outcomes[1].Identity {
		t.Fatal("distinct execution targets share an identity")
	}
	if err := pi.Exit(); err != nil {
		t.Fatal(err)
	}
	for index, outcome := range outcomes {
		want := execution.ReconcileAlive
		if index == 1 {
			want = execution.ReconcileDead
		}
		for _, byLaunch := range []bool{false, true} {
			var reconciled execution.ReconcileOutcome
			if byLaunch {
				reconciled, err = router.ReconcileLaunch(ctx, outcome.LaunchToken)
			} else {
				reconciled, err = router.Reconcile(ctx, outcome.Identity)
			}
			if err != nil || reconciled.Kind != want || reconciled.Handle != outcome.Handle || reconciled.LaunchToken != outcome.LaunchToken {
				t.Fatalf("target %d byLaunch=%v reconcile = %#v, %v", index, byLaunch, reconciled, err)
			}
		}
	}
}
