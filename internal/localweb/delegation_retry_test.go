package localweb

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestDelegationDoesNotLaunchBlockedOrManualPreparedRetry(t *testing.T) {
	for _, mode := range []string{"automatic", "blocked", "manual"} {
		t.Run(mode, func(t *testing.T) {
			server, runtime, parent, _ := delegationFixture(t)
			ctx := context.Background()
			id, _ := domain.ParseTaskID(parent.ID)
			requestJSONWithHeaders(t, server.Handler(), http.MethodPost, "/api/tasks/"+parent.ID+"/delegation", delegationPlan(), map[string]string{"Idempotency-Key": "start"}, 201, nil)
			server.dispatchDelegations(ctx)
			children, err := server.store.Reader().ListDelegationChildren(ctx, id)
			if err != nil || len(children) != 1 {
				t.Fatalf("children=%#v err=%v", children, err)
			}
			runID := children[0].RunID
			runtime.fakeSupervisor.mu.Lock()
			runtime.fakeSupervisor.terminalFiles = execution.TerminalFiles{ExitCode: 137, TerminationCause: execution.TerminationOutputLimit}
			runtime.fakeSupervisor.mu.Unlock()
			if err := runtime.Exit(); err != nil {
				t.Fatal(err)
			}
			waitForAutomaticRetryView(t, server.Handler(), runID.String(), 1, app.AutomaticRetryPending, domain.RunStateRecoveryRequired)
			failed, err := server.store.Reader().GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := server.service.PrepareRetry(ctx, app.PrepareRetryRequest{CommandMeta: commandMeta("prepare-retry"), RunID: runID, ExpectedVersion: failed.Version(), Reason: "runtime_output_limit_exceeded", Instructions: "Continue the frozen assignment.", Automatic: mode != "manual"})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "blocked" {
				if err := server.service.BlockAutomaticRetry(ctx, runID, prepared.Attempt.ID(), prepared.Run.Version(), "automatic_retry_binding_unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			server.dispatchDelegations(ctx)
			view, err := server.service.GetDelegation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "automatic" {
				if view.State != "running" {
					t.Fatalf("automatic retry was not left to its dispatcher: %#v", view)
				}
			} else if view.State != "blocked" || !strings.Contains(view.Reason, "prepared retry requiring explicit recovery") {
				t.Fatalf("prepared retry did not preserve explicit recovery: %#v", view)
			}
			current, err := server.store.Reader().GetRun(ctx, runID)
			if err != nil || current.State() != domain.RunStateReady || current.Version() != prepared.Run.Version() {
				t.Fatalf("prepared retry mutated: %#v err=%v", current, err)
			}
			if n, _ := runtime.snapshot(); n != 1 {
				t.Fatalf("delegation launched a retry using initial authority: starts=%d", n)
			}
		})
	}
}
