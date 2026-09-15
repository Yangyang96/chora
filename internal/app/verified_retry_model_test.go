package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func retryModelBinding(t *testing.T, runtime, model string) domain.ModelBinding {
	t.Helper()
	catalog, err := domain.NewModelCatalog("pi", runtime, "1", []domain.ModelIdentity{{Provider: "deepseek", ModelID: model}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestVerifiedRetryModelOverrideInheritanceAndHistory(t *testing.T) {
	ctx := context.Background()
	f := newVerificationFlowFixture(t, "fail")
	defer f.lifecycle()
	projection, err := f.db.Reader().GetTerminalProjection(ctx, f.started.Attempt.ID())
	if err != nil || len(projection.Artifacts) != 1 {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	service := app.NewService(app.Dependencies{
		Lifecycle: ctx, Store: f.db, Context: app.ContextAssembler{}, Agents: staticRegistry{adapter: verificationPiAdapter{fakeAdapter: f.adapter}}, Supervisor: f.supervisor,
		Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, Verifier: f.executor,
		PatchSource: reviewPatchSourceFake{locator: projection.Artifacts[0].Locator, patch: f.patch},
	})
	f.service = service
	first := retryModelBinding(t, "test-runtime", "deepseek-v4-pro")
	second := retryModelBinding(t, "test-runtime", "deepseek-v4-flash")
	run := f.run
	history := []domain.Attempt{f.started.Attempt}
	for index, selected := range []domain.ModelBinding{first, {}, second} {
		key := fmt.Sprintf("verified-model-%d", index)
		if _, err := service.StartVerification(ctx, app.StartVerificationRequest{CommandMeta: meta(key+"-verify", key), RunID: run.ID(), ExpectedVersion: run.Version()}); err != nil {
			t.Fatal(err)
		}
		revision := f.waitRunState(t, domain.RunStateRevisionRequired)
		request := app.PrepareVerifiedAgentRetryRequest{CommandMeta: meta(key+"-prepare", key), RunID: run.ID(), ExpectedVersion: revision.Version(), Instructions: "repair failed criterion", ModelBinding: selected}
		prepared, err := service.PrepareVerifiedAgentRetry(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		expected := selected
		if !expected.Configured() {
			expected = first
		}
		if prepared.Attempt.ModelBinding() != expected || prepared.Attempt.ExternalSession() != "" {
			t.Fatalf("successor binding/session=%#v", prepared.Attempt)
		}
		replay, err := service.PrepareVerifiedAgentRetry(ctx, request)
		if err != nil || !replay.Replayed || replay.Attempt.ID() != prepared.Attempt.ID() || replay.Attempt.ModelBinding() != expected {
			t.Fatalf("replay=%v attempt=%s model=%s err=%v", replay.Replayed, replay.Attempt.ID(), replay.Attempt.ModelBinding().JSON(), err)
		}
		changed := request
		changed.ModelBinding = second
		if expected == second {
			changed.ModelBinding = first
		}
		if _, err := service.PrepareVerifiedAgentRetry(ctx, changed); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
			t.Fatalf("changed-model replay err=%v", err)
		}
		for _, previous := range history {
			stored, err := f.db.Reader().GetAttempt(ctx, previous.ID())
			if err != nil || stored.ModelBinding() != previous.ModelBinding() {
				t.Fatalf("history binding changed: %v", err)
			}
		}
		history = append(history, prepared.Attempt)
		if index == 2 {
			break
		}
		started, err := service.StartAttempt(ctx, app.StartAttemptRequest{CommandMeta: meta(key+"-start", key), RunID: run.ID(), ExpectedVersion: prepared.Run.Version(), Mode: app.StartFresh})
		if err != nil {
			t.Fatal(err)
		}
		f.supervisor.reconcileOutcome = execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: f.supervisor.invocation.LaunchToken()}
		f.supervisor.sink.Exited()
		terminal, err := service.HandleExit(ctx, app.ExitRequest{CommandMeta: meta(key+"-exit", key), SessionID: started.Session.ID, ExpectedVersion: started.Run.Version()})
		if err != nil || terminal.Run.State() != domain.RunStateAwaitingVerification {
			t.Fatalf("terminal=%#v err=%v", terminal, err)
		}
		run = terminal.Run
	}
}
