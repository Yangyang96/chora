package trustedhost

import (
	"context"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

type frozenWorkflowTimerFactory struct{}

func (frozenWorkflowTimerFactory) NewTimer(duration time.Duration) timer {
	if duration > time.Second {
		return manualTimer{channel: make(chan time.Time)}
	}
	return realTimerFactory{}.NewTimer(duration)
}

func TestWorkflowAbsoluteDeadlineRejectsExpiredSpawn(t *testing.T) {
	platform := newFakePlatform()
	s, config, root := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "expired-workflow"}
	invocation := invocationFor(t, config, root, launch, nil, trustedEnvironment()).WithDeadline(time.Now().Add(-time.Second))
	result := s.Start(context.Background(), invocation, newRecordingSink(launch))
	if result.Kind != execution.StartProvenNoChild || platform.child != nil {
		t.Fatalf("expired Start=%+v", result)
	}
}

func TestWorkflowAbsoluteDeadlineForcesLocalTermination(t *testing.T) {
	platform := newFakePlatform()
	platform.termStops = true
	s, config, root := newTestSupervisorWithConfig(t, platform, realTimerFactory{}, 64, 10*time.Second)
	launch := execution.LaunchToken{Value: "bounded-workflow"}
	sink := newRecordingSink(launch)
	deadline := time.Now().UTC().Add(time.Second)
	result := s.Start(context.Background(), invocationFor(t, config, root, launch, nil, trustedEnvironment()).WithDeadline(deadline), sink)
	if result.Kind != execution.Started {
		t.Fatal(result)
	}
	record := s.byHandle[result.Handle.Value]
	manifest, err := readManifest(record.root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.AbsoluteDeadline == nil || !manifest.AbsoluteDeadline.Equal(deadline) {
		t.Fatalf("persisted deadline=%v", manifest.AbsoluteDeadline)
	}
	select {
	case <-sink.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("local deadline did not terminate owned group")
	}
	if len(platform.snapshotSignals()) == 0 {
		t.Fatal("deadline sent no termination signal")
	}
}

func TestWorkflowAbsoluteDeadlineSurvivesRestartAndRejectsTampering(t *testing.T) {
	platform := newFakePlatform()
	platform.termStops = true
	s, config, root := newTestSupervisor(t, platform, frozenWorkflowTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "restart-workflow"}
	deadline := time.Now().UTC().Add(2 * time.Second)
	result := s.Start(context.Background(), invocationFor(t, config, root, launch, nil, trustedEnvironment()).WithDeadline(deadline), newRecordingSink(launch))
	if result.Kind != execution.Started {
		t.Fatal(result)
	}
	record := s.byHandle[result.Handle.Value]
	original, err := readManifest(record.root)
	if err != nil {
		t.Fatal(err)
	}
	tampered := original
	later := deadline.Add(time.Minute)
	tampered.AbsoluteDeadline = &later
	if err = writeManifest(record.root, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err = newWithDependencies(config, platform, frozenWorkflowTimerFactory{}); err == nil {
		t.Fatal("deadline extension accepted on restart")
	}
	if len(platform.snapshotSignals()) != 0 {
		t.Fatal("tampered ownership sent signals")
	}
	if err = writeManifest(record.root, original); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(deadline) + time.Millisecond)
	restarted, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := restarted.Reconcile(context.Background(), result.Identity)
	if err != nil || stopped.Kind != execution.ReconcileDead {
		t.Fatalf("restart expiry=%+v %v", stopped, err)
	}
	if len(platform.snapshotSignals()) == 0 {
		t.Fatal("expired restart did not terminate owned group")
	}
	if !restarted.byHandle[result.Handle.Value].absoluteDeadline.Equal(deadline) {
		t.Fatal("restart renewed authorization window")
	}
}
