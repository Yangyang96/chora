package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestProfileSwitchRebindsSameModelWithoutChangingHistory(t *testing.T) {
	for _, mode := range []string{"inherit", "rebind", "reject different model"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			service, db, raw, room := realSpecCodingAppFixture(t, ctx)
			original := retryModelBinding(t, "local-runtime", "deepseek-v4-pro")
			rebound := retryModelBinding(t, "isolated-runtime", "deepseek-v4-pro")
			different := retryModelBinding(t, "isolated-runtime", "deepseek-v4-flash")
			create := realSpecCodingCreateRequest(room)
			create.ModelBinding = original
			created, err := service.CreateTask(ctx, create)
			if err != nil {
				t.Fatal(err)
			}
			accepted := submitAndAcceptRealTask(t, ctx, service, created, "model-profile-switch")
			envelope, err := newRealSpecCodingTestEnvelope(room.repositoryRoot)
			if err != nil {
				t.Fatal(err)
			}
			service = app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{&fakeAdapter{id: "pi"}}, Supervisor: &fakeSupervisor{}, Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{}, SpecCodingEnvelopeResolver: fixedSpecCodingEnvelopeResolver{envelope}})
			run, err := service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("create-model-switch-run", "create"), TaskID: created.Task.ID(), RevisionID: accepted.Review.RevisionID()})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := service.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-model-switch", "prepare"), RunID: run.Run.ID(), ExpectedVersion: run.Run.Version()})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Attempt.ModelBinding() != original {
				t.Fatal("fixture model binding lost")
			}
			if _, err := raw.ExecContext(ctx, `UPDATE runs SET state='revision_required' WHERE id=?`, run.Run.ID().String()); err != nil {
				t.Fatal(err)
			}
			request := app.PrepareProfileSwitchRequest{CommandMeta: meta("switch-model-profile", "switch"), RunID: run.Run.ID(), ExpectedVersion: prepared.Run.Version(), Profile: domain.AgentExecutionProfileMinimal, Reason: "use isolated execution"}
			expected := original
			switch mode {
			case "rebind":
				request.ModelBinding = rebound
				expected = rebound
			case "reject different model":
				request.ModelBinding = different
			}
			switched, err := service.PrepareProfileSwitch(ctx, request)
			if mode == "reject different model" {
				if !errors.Is(err, app.ErrInvalidCommand) {
					t.Fatalf("different-model switch err=%v", err)
				}
				current, readErr := db.Reader().GetCurrentAttempt(ctx, run.Run.ID())
				if readErr != nil || current.ID() != prepared.Attempt.ID() || current.ModelBinding() != original {
					t.Fatalf("failed switch mutated attempt: %v", readErr)
				}
				preference, readErr := db.Reader().GetTaskAgentExecutionProfilePreference(ctx, created.Task.ID())
				if readErr != nil || preference.Version() != 1 || preference.Profile() != domain.AgentExecutionProfileTrustedLocal {
					t.Fatalf("failed switch changed preference: %#v %v", preference, readErr)
				}
				return
			}
			if err != nil || switched.Attempt.ModelBinding() != expected || switched.Attempt.ExternalSession() != "" {
				t.Fatalf("switch=%#v err=%v", switched, err)
			}
			replay, err := service.PrepareProfileSwitch(ctx, request)
			if err != nil || !replay.Replayed || replay.Attempt.ID() != switched.Attempt.ID() || replay.Attempt.ModelBinding() != expected {
				t.Fatalf("replay=%v attempt=%s model=%s err=%v", replay.Replayed, replay.Attempt.ID(), replay.Attempt.ModelBinding().JSON(), err)
			}
			conflict := request
			conflict.ModelBinding = different
			if _, err := service.PrepareProfileSwitch(ctx, conflict); !errors.Is(err, storecontract.ErrIdempotencyConflict) {
				t.Fatalf("model idempotency conflict err=%v", err)
			}
			old, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
			if err != nil || old.ModelBinding() != original || old.AgentExecutionProfileBinding() != prepared.Attempt.AgentExecutionProfileBinding() {
				t.Fatalf("predecessor mutated: %v", err)
			}
			charter, err := db.Reader().GetCharter(ctx, accepted.Charter.ID())
			if err != nil || charter.ModelBinding() != original {
				t.Fatalf("charter mutated: %v", err)
			}
		})
	}
}
