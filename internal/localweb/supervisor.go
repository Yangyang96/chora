package localweb

import (
	"context"
	"fmt"
	"sync"

	"github.com/Yangyang96/chora/internal/execution"
)

type routingSupervisor struct {
	mu sync.RWMutex

	// byTarget is the sole Start and exact-restore registry. byAdapter exists
	// only for legacy Fake/dev injection while server composition migrates.
	byTarget  map[execution.ExecutionTarget]execution.ProcessSupervisor
	byAdapter map[string]execution.ProcessSupervisor

	byHandle   map[string]execution.ExecutionTarget
	byIdentity map[string]execution.ExecutionTarget
	byLaunch   map[string]execution.ExecutionTarget
	routes     map[string]supervisorRoute
}

type supervisorRoute struct {
	target   execution.ExecutionTarget
	identity string
	launch   string
}

// newTargetRoutingSupervisor constructs the exact production router. Registry
// entries are cloned and can only be selected by an Invocation's full target.
func newTargetRoutingSupervisor(supervisors map[execution.ExecutionTarget]execution.ProcessSupervisor) (*routingSupervisor, error) {
	registry := make(map[execution.ExecutionTarget]execution.ProcessSupervisor, len(supervisors))
	for target, processSupervisor := range supervisors {
		if !target.Valid() || processSupervisor == nil {
			return nil, fmt.Errorf("invalid supervisor registration for target %q/%q", target.AdapterID(), target.ProviderID())
		}
		registry[target] = processSupervisor
	}
	return &routingSupervisor{
		byTarget: registry, byAdapter: make(map[string]execution.ProcessSupervisor),
		byHandle: make(map[string]execution.ExecutionTarget), byIdentity: make(map[string]execution.ExecutionTarget),
		byLaunch: make(map[string]execution.ExecutionTarget), routes: make(map[string]supervisorRoute),
	}, nil
}

// newRoutingSupervisor is a compatibility constructor for historical Fake and
// dev composition. Every entry becomes only adapter/provider==adapter; it never
// aliases Pi to docker or trusted_host.
func newRoutingSupervisor(supervisors map[string]execution.ProcessSupervisor) *routingSupervisor {
	targets := make(map[execution.ExecutionTarget]execution.ProcessSupervisor, len(supervisors))
	legacy := make(map[string]execution.ProcessSupervisor, len(supervisors))
	for adapterID, processSupervisor := range supervisors {
		target, err := execution.NewExecutionTarget(adapterID, adapterID)
		if err != nil || processSupervisor == nil {
			panic(fmt.Sprintf("invalid legacy supervisor registration for adapter %q", adapterID))
		}
		targets[target] = processSupervisor
		legacy[adapterID] = processSupervisor
	}
	router, err := newTargetRoutingSupervisor(targets)
	if err != nil {
		panic(err)
	}
	router.byAdapter = legacy
	return router
}

// registerTarget adds or replaces one exact adapter/provider route only while
// no live handle is bound to it. It is used by server composition and explicit
// test fixtures; it never creates an adapter-only alias.
func (supervisor *routingSupervisor) registerTarget(target execution.ExecutionTarget, processSupervisor execution.ProcessSupervisor) error {
	if !target.Valid() || processSupervisor == nil {
		return fmt.Errorf("invalid supervisor registration for target %q/%q", target.AdapterID(), target.ProviderID())
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	for _, route := range supervisor.routes {
		if route.target == target {
			return fmt.Errorf("cannot replace execution target %q/%q while it owns a runtime handle", target.AdapterID(), target.ProviderID())
		}
	}
	supervisor.byTarget[target] = processSupervisor
	return nil
}

func (supervisor *routingSupervisor) hasTarget(target execution.ExecutionTarget) bool {
	if supervisor == nil || !target.Valid() {
		return false
	}
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return supervisor.byTarget[target] != nil
}

func (supervisor *routingSupervisor) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	target := invocation.Target()
	processSupervisor := supervisor.forTarget(target)
	if processSupervisor == nil {
		return execution.StartOutcome{
			Kind: execution.StartProvenNoChild, LaunchToken: invocation.LaunchToken(),
			Diagnostic: fmt.Sprintf("supervisor for execution target %q/%q is disabled", target.AdapterID(), target.ProviderID()),
		}
	}
	outcome := processSupervisor.Start(ctx, invocation, sink)
	if (outcome.Kind == execution.Started || outcome.Kind == execution.StartReconciliationRequired) && outcome.Handle.Valid() && outcome.LaunchToken == invocation.LaunchToken() {
		if err := supervisor.recordRoute(target, outcome.Handle, outcome.Identity, outcome.LaunchToken); err != nil {
			outcome.Kind = execution.StartReconciliationRequired
			outcome.Diagnostic = joinSupervisorDiagnostic(outcome.Diagnostic, err.Error())
		}
	}
	return outcome
}

// RestoreTarget restores a route only through its exact adapter/provider pair.
// A valid target-owned outcome may be alive, dead, or uncertain; the router
// records no ownership unless the handle and launch binding are exact.
func (supervisor *routingSupervisor) RestoreTarget(ctx context.Context, target execution.ExecutionTarget, identity execution.ProcessIdentity, launch execution.LaunchToken) error {
	if !target.Valid() || !launch.Valid() {
		return fmt.Errorf("restored route requires exact execution target and launch token")
	}
	processSupervisor := supervisor.forTarget(target)
	if processSupervisor == nil {
		return fmt.Errorf("supervisor for execution target %q/%q is disabled", target.AdapterID(), target.ProviderID())
	}
	var (
		outcome execution.ReconcileOutcome
		err     error
	)
	if identity.Valid() {
		outcome, err = processSupervisor.Reconcile(ctx, identity)
	} else {
		outcome, err = processSupervisor.ReconcileLaunch(ctx, launch)
	}
	if err != nil {
		return err
	}
	if !validReconcileKind(outcome.Kind) || !outcome.Handle.Valid() || outcome.LaunchToken != launch {
		return fmt.Errorf("restored route outcome does not prove exact target ownership")
	}
	return supervisor.recordRoute(target, outcome.Handle, identity, launch)
}

// Restore is the legacy adapter/provider==adapter wrapper. Product Pi routing
// must use RestoreTarget with docker or trusted_host explicitly.
func (supervisor *routingSupervisor) Restore(ctx context.Context, adapterID string, identity execution.ProcessIdentity, launch execution.LaunchToken) error {
	target, err := execution.NewExecutionTarget(adapterID, adapterID)
	if err != nil {
		return err
	}
	return supervisor.RestoreTarget(ctx, target, identity, launch)
}

func validReconcileKind(kind execution.ReconcileOutcomeKind) bool {
	switch kind {
	case execution.ReconcileAlive, execution.ReconcileDead, execution.ReconcileUncertain:
		return true
	default:
		return false
	}
}

func (supervisor *routingSupervisor) recordRoute(target execution.ExecutionTarget, handle execution.RuntimeHandle, identity execution.ProcessIdentity, launch execution.LaunchToken) error {
	if !target.Valid() || !handle.Valid() || !launch.Valid() {
		return fmt.Errorf("runtime route requires exact target, handle, and launch token")
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if existing, ok := supervisor.byHandle[handle.Value]; ok && existing != target {
		return fmt.Errorf("runtime handle is already owned by another execution target")
	}
	if existing, ok := supervisor.routes[handle.Value]; ok && (existing.target != target || existing.identity != identity.Value || existing.launch != launch.Value) {
		return fmt.Errorf("runtime handle route binding conflicts with an existing route")
	}
	for existingHandle, existing := range supervisor.routes {
		if existingHandle == handle.Value {
			continue
		}
		if identity.Valid() && existing.identity == identity.Value {
			return fmt.Errorf("process identity is already bound to another runtime handle")
		}
		if existing.launch == launch.Value {
			return fmt.Errorf("launch token is already bound to another runtime handle")
		}
	}
	if identity.Valid() {
		if existing, ok := supervisor.byIdentity[identity.Value]; ok && existing != target {
			return fmt.Errorf("process identity is already owned by another execution target")
		}
	}
	if existing, ok := supervisor.byLaunch[launch.Value]; ok && existing != target {
		return fmt.Errorf("launch token is already owned by another execution target")
	}
	supervisor.byHandle[handle.Value] = target
	supervisor.routes[handle.Value] = supervisorRoute{target: target, identity: identity.Value, launch: launch.Value}
	if identity.Valid() {
		supervisor.byIdentity[identity.Value] = target
	}
	supervisor.byLaunch[launch.Value] = target
	return nil
}

func (supervisor *routingSupervisor) Stop(ctx context.Context, handle execution.RuntimeHandle, intent execution.StopIntent) (execution.StopOutcome, error) {
	processSupervisor, _, err := supervisor.forHandle(handle)
	if err != nil {
		return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: err.Error()}, nil
	}
	return processSupervisor.Stop(ctx, handle, intent)
}

func (supervisor *routingSupervisor) Reconcile(ctx context.Context, identity execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.RLock()
	target, ok := supervisor.byIdentity[identity.Value]
	supervisor.mu.RUnlock()
	if !ok {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "unknown process identity"}, nil
	}
	processSupervisor := supervisor.forTarget(target)
	if processSupervisor == nil {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: "execution target is no longer registered"}, nil
	}
	return processSupervisor.Reconcile(ctx, identity)
}

func (supervisor *routingSupervisor) ReconcileLaunch(ctx context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	supervisor.mu.RLock()
	target, ok := supervisor.byLaunch[launch.Value]
	supervisor.mu.RUnlock()
	if !ok {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, LaunchToken: launch, Diagnostic: "unknown launch token"}, nil
	}
	processSupervisor := supervisor.forTarget(target)
	if processSupervisor == nil {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, LaunchToken: launch, Diagnostic: "execution target is no longer registered"}, nil
	}
	return processSupervisor.ReconcileLaunch(ctx, launch)
}

func (supervisor *routingSupervisor) Read(ctx context.Context, handle execution.RuntimeHandle, kind execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	processSupervisor, _, err := supervisor.forHandle(handle)
	if err != nil {
		return execution.StreamChunk{}, err
	}
	return processSupervisor.Read(ctx, handle, kind, offset, limit)
}

func (supervisor *routingSupervisor) Drain(ctx context.Context, handle execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	processSupervisor, _, err := supervisor.forHandle(handle)
	if err != nil {
		return execution.DrainOutcome{}, err
	}
	return processSupervisor.Drain(ctx, handle, offsets, limit)
}

// CloseStdin routes a stdin-close signal to the owning supervisor when it
// implements execution.StdinCloser; it is a no-op for supervisors that deliver
// stdin up front.
func (supervisor *routingSupervisor) CloseStdin(ctx context.Context, handle execution.RuntimeHandle) error {
	processSupervisor, _, err := supervisor.forHandle(handle)
	if err != nil {
		return err
	}
	closer, ok := processSupervisor.(execution.StdinCloser)
	if !ok {
		return nil
	}
	return closer.CloseStdin(ctx, handle)
}

func (supervisor *routingSupervisor) Finalize(ctx context.Context, handle execution.RuntimeHandle, policy execution.RetentionPolicy) error {
	processSupervisor, target, err := supervisor.forHandle(handle)
	if err != nil {
		return err
	}
	if err := processSupervisor.Finalize(ctx, handle, policy); err != nil {
		return err
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	route, ok := supervisor.routes[handle.Value]
	if !ok || route.target != target {
		return nil
	}
	delete(supervisor.byHandle, handle.Value)
	delete(supervisor.routes, handle.Value)
	if route.identity != "" && supervisor.byIdentity[route.identity] == target {
		delete(supervisor.byIdentity, route.identity)
	}
	if route.launch != "" && supervisor.byLaunch[route.launch] == target {
		delete(supervisor.byLaunch, route.launch)
	}
	return nil
}

func (supervisor *routingSupervisor) forTarget(target execution.ExecutionTarget) execution.ProcessSupervisor {
	if !target.Valid() {
		return nil
	}
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	processSupervisor := supervisor.byTarget[target]
	// Historical Fake/dev fixtures replace byAdapter entries directly. Honor
	// that seam only for the exact adapter/provider==adapter compatibility
	// target; it cannot select docker or trusted_host for Pi.
	if target.AdapterID() == target.ProviderID() {
		if legacy := supervisor.byAdapter[target.AdapterID()]; legacy != nil {
			processSupervisor = legacy
			supervisor.byTarget[target] = legacy
		}
	}
	return processSupervisor
}

func (supervisor *routingSupervisor) forHandle(handle execution.RuntimeHandle) (execution.ProcessSupervisor, execution.ExecutionTarget, error) {
	supervisor.mu.RLock()
	target, ok := supervisor.byHandle[handle.Value]
	processSupervisor := supervisor.byTarget[target]
	supervisor.mu.RUnlock()
	if !ok || processSupervisor == nil {
		return nil, execution.ExecutionTarget{}, fmt.Errorf("unknown runtime handle")
	}
	return processSupervisor, target, nil
}

func joinSupervisorDiagnostic(current, next string) string {
	if current == "" {
		return next
	}
	return current + "; " + next
}

var _ execution.ProcessSupervisor = (*routingSupervisor)(nil)
