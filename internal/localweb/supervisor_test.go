package localweb

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestRoutingSupervisorRoutesLifecycleByRecordedIdentity(t *testing.T) {
	target := newFakeSupervisor()
	router := newRoutingSupervisor(map[string]execution.ProcessSupervisor{"fake": target})
	launch := execution.LaunchToken{Value: "launch-routing-test"}
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: "fake", Executable: "/fake", WorkingRoot: t.TempDir(), LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := router.Start(context.Background(), invocation, routingTestSink{launch: launch})
	if outcome.Kind != execution.Started || !outcome.Handle.Valid() || !outcome.Identity.Valid() {
		t.Fatalf("start outcome = %#v", outcome)
	}
	reconciled, err := router.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileAlive || reconciled.Handle != outcome.Handle || reconciled.LaunchToken != launch {
		t.Fatalf("alive reconcile = %#v, %v", reconciled, err)
	}
	if err := target.Exit(); err != nil {
		t.Fatal(err)
	}
	reconciled, err = router.ReconcileLaunch(context.Background(), launch)
	if err != nil || reconciled.Kind != execution.ReconcileDead || reconciled.Handle != outcome.Handle {
		t.Fatalf("dead reconcile = %#v, %v", reconciled, err)
	}
	if err := router.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	unknown, err := router.Reconcile(context.Background(), outcome.Identity)
	if err != nil || unknown.Kind != execution.ReconcileUncertain {
		t.Fatalf("finalized reconcile = %#v, %v", unknown, err)
	}
}

func TestRoutingSupervisorRestoresDeadLifecycleRoute(t *testing.T) {
	target := newFakeSupervisor()
	launch := execution.LaunchToken{Value: "launch-routing-recovery"}
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: "fake", Executable: "/fake", WorkingRoot: t.TempDir(), LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := target.Start(context.Background(), invocation, routingTestSink{launch: launch})
	if started.Kind != execution.Started {
		t.Fatalf("start outcome = %#v", started)
	}
	if err := target.Exit(); err != nil {
		t.Fatal(err)
	}

	restartedRouter := newRoutingSupervisor(map[string]execution.ProcessSupervisor{"fake": target})
	if err := restartedRouter.Restore(context.Background(), "fake", started.Identity, launch); err != nil {
		t.Fatal(err)
	}
	reconciled, err := restartedRouter.Reconcile(context.Background(), started.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileDead || reconciled.Handle != started.Handle || reconciled.LaunchToken != launch {
		t.Fatalf("restored reconcile = %#v, %v", reconciled, err)
	}
	if _, err := restartedRouter.Drain(context.Background(), started.Handle, execution.StreamOffsets{}, 1024); err != nil {
		t.Fatal(err)
	}
	if err := restartedRouter.Finalize(context.Background(), started.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingSupervisorRoutesSamePiAdapterByExactProvider(t *testing.T) {
	docker := newRoutingProbeSupervisor()
	trusted := newRoutingProbeSupervisor()
	dockerTarget := mustExecutionTarget(t, "pi", "docker")
	trustedTarget := mustExecutionTarget(t, "pi", "trusted_host")
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{
		dockerTarget: docker, trustedTarget: trusted,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		target execution.ExecutionTarget
		want   *routingProbeSupervisor
	}{
		{name: "managed", target: dockerTarget, want: docker},
		{name: "trusted", target: trustedTarget, want: trusted},
	} {
		t.Run(test.name, func(t *testing.T) {
			launch := execution.LaunchToken{Value: "launch-" + test.name}
			invocation := mustRoutingInvocation(t, test.target, launch)
			outcome := router.Start(context.Background(), invocation, routingTestSink{launch: launch})
			if outcome.Kind != execution.Started || !outcome.Handle.Valid() {
				t.Fatalf("start outcome = %#v", outcome)
			}
		})
	}
	if docker.startCount() != 1 || trusted.startCount() != 1 {
		t.Fatalf("exact start calls docker=%d trusted=%d", docker.startCount(), trusted.startCount())
	}
}

func TestRoutingSupervisorWrongProviderProvesNoChildWithoutFallback(t *testing.T) {
	docker := newRoutingProbeSupervisor()
	trusted := newRoutingProbeSupervisor()
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{
		mustExecutionTarget(t, "pi", "docker"):       docker,
		mustExecutionTarget(t, "pi", "trusted_host"): trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	launch := execution.LaunchToken{Value: "launch-wrong-provider"}
	outcome := router.Start(context.Background(), mustRoutingInvocation(t, mustExecutionTarget(t, "pi", "other"), launch), routingTestSink{launch: launch})
	if outcome.Kind != execution.StartProvenNoChild || outcome.LaunchToken != launch {
		t.Fatalf("wrong-provider outcome = %#v", outcome)
	}
	if docker.startCount() != 0 || trusted.startCount() != 0 {
		t.Fatalf("wrong provider called a supervisor: docker=%d trusted=%d", docker.startCount(), trusted.startCount())
	}
}

func TestRoutingSupervisorRestoreIsExactAndAcceptsOwnedKinds(t *testing.T) {
	for _, kind := range []execution.ReconcileOutcomeKind{execution.ReconcileAlive, execution.ReconcileDead, execution.ReconcileUncertain} {
		t.Run(string(kind), func(t *testing.T) {
			launch := execution.LaunchToken{Value: "restore-" + string(kind)}
			handle := execution.RuntimeHandle{Value: "handle-" + string(kind)}
			docker := newRoutingProbeSupervisor()
			trusted := newRoutingProbeSupervisor()
			trusted.restoreOutcome = &execution.ReconcileOutcome{Kind: kind, Handle: handle, LaunchToken: launch}
			dockerTarget := mustExecutionTarget(t, "pi", "docker")
			trustedTarget := mustExecutionTarget(t, "pi", "trusted_host")
			router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{
				dockerTarget: docker, trustedTarget: trusted,
			})
			if err != nil {
				t.Fatal(err)
			}
			identity := execution.ProcessIdentity{Value: "identity-" + string(kind)}
			if err := router.RestoreTarget(context.Background(), trustedTarget, identity, launch); err != nil {
				t.Fatal(err)
			}
			if docker.reconcileCount() != 0 || trusted.reconcileCount() != 1 {
				t.Fatalf("restore calls docker=%d trusted=%d", docker.reconcileCount(), trusted.reconcileCount())
			}
			if _, err := router.Drain(context.Background(), handle, execution.StreamOffsets{}, 1); err != nil {
				t.Fatalf("restored handle is not routed: %v", err)
			}
		})
	}
}

func TestRoutingSupervisorRestoreRejectsWrongTargetAndMismatchedLaunch(t *testing.T) {
	launch := execution.LaunchToken{Value: "restore-exact"}
	docker := newRoutingProbeSupervisor()
	docker.restoreOutcome = &execution.ReconcileOutcome{
		Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "restore-handle"},
		LaunchToken: execution.LaunchToken{Value: "different-launch"},
	}
	dockerTarget := mustExecutionTarget(t, "pi", "docker")
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{dockerTarget: docker})
	if err != nil {
		t.Fatal(err)
	}
	identity := execution.ProcessIdentity{Value: "restore-identity"}
	if err := router.RestoreTarget(context.Background(), mustExecutionTarget(t, "pi", "trusted_host"), identity, launch); err == nil {
		t.Fatal("restored through an unregistered provider")
	}
	if docker.reconcileCount() != 0 {
		t.Fatal("wrong target fell back to the registered Docker supervisor")
	}
	if err := router.RestoreTarget(context.Background(), dockerTarget, identity, launch); err == nil {
		t.Fatal("accepted restore outcome with mismatched launch")
	}
	if _, err := router.Drain(context.Background(), execution.RuntimeHandle{Value: "restore-handle"}, execution.StreamOffsets{}, 1); err == nil {
		t.Fatal("mismatched restore fabricated handle ownership")
	}
}

func TestRoutingSupervisorConcurrentExactLifecycle(t *testing.T) {
	docker := newRoutingProbeSupervisor()
	trusted := newRoutingProbeSupervisor()
	targets := []execution.ExecutionTarget{
		mustExecutionTarget(t, "pi", "docker"), mustExecutionTarget(t, "pi", "trusted_host"),
	}
	router, err := newTargetRoutingSupervisor(map[execution.ExecutionTarget]execution.ProcessSupervisor{
		targets[0]: docker, targets[1]: trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			launch := execution.LaunchToken{Value: fmt.Sprintf("concurrent-%d", index)}
			outcome := router.Start(context.Background(), mustRoutingInvocation(t, targets[index%len(targets)], launch), routingTestSink{launch: launch})
			if outcome.Kind != execution.Started {
				t.Errorf("start %d = %#v", index, outcome)
				return
			}
			if reconciled, err := router.Reconcile(context.Background(), outcome.Identity); err != nil || reconciled.Handle != outcome.Handle {
				t.Errorf("reconcile %d = %#v, %v", index, reconciled, err)
				return
			}
			if err := router.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
				t.Errorf("finalize %d: %v", index, err)
			}
		}()
	}
	wait.Wait()
	if docker.startCount() != 32 || trusted.startCount() != 32 {
		t.Fatalf("concurrent starts docker=%d trusted=%d", docker.startCount(), trusted.startCount())
	}
}

func mustExecutionTarget(t *testing.T, adapterID, providerID string) execution.ExecutionTarget {
	t.Helper()
	target, err := execution.NewExecutionTarget(adapterID, providerID)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func mustRoutingInvocation(t *testing.T, target execution.ExecutionTarget, launch execution.LaunchToken) execution.Invocation {
	t.Helper()
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: target.AdapterID(), Target: target, Executable: "/fake", WorkingRoot: t.TempDir(), LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

type routingProbeSupervisor struct {
	mu             sync.Mutex
	starts         int
	reconciles     int
	restoreOutcome *execution.ReconcileOutcome
	byIdentity     map[string]execution.ReconcileOutcome
	byLaunch       map[string]execution.ReconcileOutcome
}

func newRoutingProbeSupervisor() *routingProbeSupervisor {
	return &routingProbeSupervisor{byIdentity: make(map[string]execution.ReconcileOutcome), byLaunch: make(map[string]execution.ReconcileOutcome)}
}

func (supervisor *routingProbeSupervisor) Start(_ context.Context, invocation execution.Invocation, _ execution.RuntimeSink) execution.StartOutcome {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.starts++
	launch := invocation.LaunchToken()
	handle := execution.RuntimeHandle{Value: invocation.Target().ProviderID() + ":handle:" + launch.Value}
	identity := execution.ProcessIdentity{Value: invocation.Target().ProviderID() + ":identity:" + launch.Value}
	reconciled := execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: handle, LaunchToken: launch}
	supervisor.byIdentity[identity.Value] = reconciled
	supervisor.byLaunch[launch.Value] = reconciled
	return execution.StartOutcome{Kind: execution.Started, Handle: handle, Identity: identity, LaunchToken: launch}
}

func (*routingProbeSupervisor) Stop(context.Context, execution.RuntimeHandle, execution.StopIntent) (execution.StopOutcome, error) {
	return execution.StopOutcome{Kind: execution.StopConfirmed}, nil
}

func (supervisor *routingProbeSupervisor) Reconcile(_ context.Context, identity execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.reconciles++
	if supervisor.restoreOutcome != nil {
		return *supervisor.restoreOutcome, nil
	}
	return supervisor.byIdentity[identity.Value], nil
}

func (supervisor *routingProbeSupervisor) ReconcileLaunch(_ context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.restoreOutcome != nil {
		return *supervisor.restoreOutcome, nil
	}
	return supervisor.byLaunch[launch.Value], nil
}

func (*routingProbeSupervisor) Read(context.Context, execution.RuntimeHandle, execution.StreamKind, int64, int) (execution.StreamChunk, error) {
	return execution.StreamChunk{}, nil
}

func (*routingProbeSupervisor) Drain(context.Context, execution.RuntimeHandle, execution.StreamOffsets, int) (execution.DrainOutcome, error) {
	return execution.DrainOutcome{}, nil
}

func (*routingProbeSupervisor) Finalize(context.Context, execution.RuntimeHandle, execution.RetentionPolicy) error {
	return nil
}

func (supervisor *routingProbeSupervisor) startCount() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.starts
}

func (supervisor *routingProbeSupervisor) reconcileCount() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.reconciles
}

type routingTestSink struct{ launch execution.LaunchToken }

func (sink routingTestSink) Binding() execution.LaunchToken { return sink.launch }
func (routingTestSink) Notify(execution.StreamKind, int64)  {}
func (routingTestSink) Exited()                             {}
