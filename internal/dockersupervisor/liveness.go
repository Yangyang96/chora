package dockersupervisor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

// CLI transport liveness does not prove that its container is still running.
// Keep the trusted exit/collection path authoritative for death and cleanup.
func (supervisor *Supervisor) reconcileObserved(ctx context.Context, record *attemptRecord) execution.ReconcileOutcome {
	outcome := reconcile(record)
	if record == nil || outcome.Kind != execution.ReconcileAlive || supervisor.config.Workbench == nil {
		return outcome
	}
	record.mu.Lock()
	collecting, started, container := record.exitObserved, record.startReturned, record.containerDockerID
	record.mu.Unlock()
	if collecting || !started {
		return outcome
	}
	record.livenessMu.Lock()
	defer record.livenessMu.Unlock()
	if !record.livenessAt.IsZero() && time.Since(record.livenessAt) < 2*time.Second {
		return record.liveness
	}
	observationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := supervisor.config.Runner.Run(observationCtx, Command{Args: []string{"container", "inspect", "--format", "{{json .State}}", container}})
	var state struct {
		Running    *bool
		Paused     bool
		Restarting bool
		Dead       bool
	}
	if container == "" || err != nil || result.ExitCode != 0 || json.Unmarshal(result.Stdout, &state) != nil || state.Running == nil {
		outcome.Kind = execution.ReconcileUncertain
		outcome.Diagnostic = "Docker container state is unavailable; execution liveness cannot be confirmed"
	} else if !*state.Running || state.Paused || state.Restarting || state.Dead {
		outcome.Kind = execution.ReconcileUncertain
		outcome.Diagnostic = "Docker container stopped or became unavailable before runtime exit was confirmed"
	}
	// The waiter may have entered collection while inspect was in flight. Its
	// pause/stop is expected; do not convert normal settlement into intervention.
	record.mu.Lock()
	collecting = record.exitObserved
	record.mu.Unlock()
	if collecting {
		return reconcile(record)
	}
	record.livenessAt, record.liveness = time.Now(), outcome
	return outcome
}
