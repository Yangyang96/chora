package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/verifier"
)

type continuityVerifier struct {
	authority app.VerificationAuthority
	started   chan context.Context
	release   chan struct{}
}

func (v *continuityVerifier) Authority() app.VerificationAuthority { return v.authority }
func (v *continuityVerifier) Verify(ctx context.Context, request app.VerificationExecutionRequest) (verifier.AttemptEvidence, error) {
	v.started <- ctx
	select {
	case <-v.release:
		return fakeVerificationEvidence(request, "pass"), nil
	case <-ctx.Done():
		return fakeVerificationEvidence(request, "cancelled"), nil
	}
}

// Browser requests may disappear after authorization, and separate tabs may
// submit distinct command keys for the same observed Run version. Execution
// belongs to the service lifecycle and must survive both cases exactly once.
func TestVerificationContinuesOnceAfterConcurrentRequestsDisconnect(t *testing.T) {
	fixture := newVerificationFlowFixture(t, "pass")
	defer fixture.lifecycle()
	lifecycle, stop := context.WithCancel(context.Background())
	defer stop()
	executor := &continuityVerifier{authority: fixture.executor.authority, started: make(chan context.Context, 2), release: make(chan struct{})}
	service := app.NewService(app.Dependencies{Lifecycle: lifecycle, Store: fixture.db,
		Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}, Verifier: executor})
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, key := range []string{"first-tab", "second-tab"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := service.StartVerification(ctx, app.StartVerificationRequest{CommandMeta: meta(key, key), RunID: fixture.run.ID(), ExpectedVersion: fixture.run.Version()})
			errors <- err
		}(key)
	}
	wg.Wait()
	close(errors)
	successes := 0
	for err := range errors {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful verification starts = %d, want one", successes)
	}
	disconnect()
	select {
	case executionContext := <-executor.started:
		if err := executionContext.Err(); err != nil {
			t.Fatalf("request disconnect cancelled authorized verification: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authorized verification did not start")
	}
	close(executor.release)
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	select {
	case <-executor.started:
		t.Fatal("verification executed more than once")
	default:
	}
}

func TestAutomaticVerificationTerminalReplayDoesNotExecuteAgain(t *testing.T) {
	fixture := newVerificationFlowFixtureOpts(t, "pass", true)
	defer fixture.lifecycle()
	fixture.waitRunState(t, domain.RunStateAwaitingReview)
	for range 2 {
		replayed, err := fixture.service.HandleExit(context.Background(), app.ExitRequest{
			CommandMeta: meta("terminal-verification", "terminal-verification"), SessionID: fixture.started.Session.ID, ExpectedVersion: fixture.started.Run.Version(),
		})
		if err != nil || !replayed.Replayed {
			t.Fatalf("terminal replay = %#v, %v", replayed, err)
		}
	}
	if executions := len(fixture.executor.started); executions != 1 {
		t.Fatalf("verification executions = %d, want one", executions)
	}
}
