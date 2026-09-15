package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestRetryRetainsSelectedModelWithoutRewritingHistory(t *testing.T) {
	f := newFixture(t)
	first := f.started(t)
	failed := failAttempt(t, f, first, "model-failure-1", execution.TerminationOutputLimit)
	catalog, err := domain.NewModelCatalog("pi", "test-runtime", "1", []domain.ModelIdentity{{Provider: "p", ModelID: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("model-retry-1", "1"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(), Reason: "change model", Instructions: "retry", ModelBinding: selected})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Attempt.ModelBinding() != selected {
		t.Fatal("explicit retry model lost")
	}
	second := startPreparedRetry(t, f, retry, "model-start-2")
	failed = failAttempt(t, f, second, "model-failure-2", execution.TerminationOutputLimit)
	successor, err := f.service.PrepareRetry(context.Background(), app.PrepareRetryRequest{CommandMeta: meta("model-retry-2", "2"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(), Reason: "retry", Instructions: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if successor.Attempt.ModelBinding() != selected {
		t.Fatal("retry reverted to charter model")
	}
	old, err := f.db.Reader().GetAttempt(context.Background(), first.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	if old.ModelBinding().Configured() {
		t.Fatal("first Attempt was rewritten")
	}
	persisted, err := f.db.Reader().GetAttempt(context.Background(), retry.Attempt.ID())
	if err != nil || persisted.ModelBinding() != selected {
		t.Fatalf("persisted selection lost: %v", err)
	}
}

func TestAutomaticRetryInheritsLatestSelectedModel(t *testing.T) {
	f := newFixture(t)
	first := f.started(t)
	failed := failAttempt(t, f, first, "model-auto-failure-1", execution.TerminationOutputLimit)
	catalog, err := domain.NewModelCatalog("pi", "runtime", "1", []domain.ModelIdentity{{Provider: "p", ModelID: "selected"}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := domain.NewModelBinding(catalog, catalog.Models[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request := app.PrepareRetryRequest{CommandMeta: meta("model-auto-switch", "switch"), RunID: failed.Run.ID(), ExpectedVersion: failed.Run.Version(), Reason: "select", Instructions: "retry", ModelBinding: selected}
	retry, err := f.service.PrepareRetry(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.PrepareRetry(context.Background(), request)
	if err != nil || replay.Attempt.ModelBinding() != selected {
		t.Fatalf("replay lost model: %v", err)
	}
	enableAutomaticRetry(t, f)
	second := startPreparedRetry(t, f, retry, "model-auto-start-2")
	failed = failAttempt(t, f, second, "model-auto-failure-2", execution.TerminationOutputLimit)
	automatic := prepareAutomaticRetry(t, f, failed, "model-auto-retry")
	if automatic.Attempt.ModelBinding() != selected {
		t.Fatal("automatic retry reverted model")
	}
	old, err := f.db.Reader().GetAttempt(context.Background(), first.Attempt.ID())
	if err != nil || old.ModelBinding().Configured() {
		t.Fatal("automatic retry rewrote history")
	}
}
