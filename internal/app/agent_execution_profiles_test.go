package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestTrustedLocalRequiresCurrentAcknowledgementBeforeAnyTaskSideEffect(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	newer, err := domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
		PolicyVersion: "chora.trusted-local-disclosure.v2", ActorID: "owner", SessionID: "newer-policy", AcknowledgedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTrustedLocalAcknowledgement(ctx, newer)
	}); err != nil {
		t.Fatal(err)
	}
	request := realSpecCodingCreateRequest(room)
	request.AgentExecutionProfile = domain.AgentExecutionProfileTrustedLocal
	if _, err := service.CreateTask(ctx, request); !errors.Is(err, app.ErrUnauthorizedCommand) {
		t.Fatalf("Trusted Local without current acknowledgement error=%v", err)
	}
	assertRawCounts(t, raw, map[string]int{
		"tasks": 0, "agent_execution_profile_preferences": 0, "user_spec_coding_intents": 0,
	})

	acknowledged, err := service.AcknowledgeTrustedLocal(ctx, app.AcknowledgeTrustedLocalRequest{
		CommandMeta: meta("ack-trusted-local", "ack-trusted-local"), PolicyVersion: domain.TrustedLocalDisclosurePolicy,
	})
	if err != nil || acknowledged.Acknowledgement.PolicyVersion() != domain.TrustedLocalDisclosurePolicy {
		t.Fatalf("acknowledgement=%#v err=%v", acknowledged, err)
	}
	created, err := service.CreateTask(ctx, request)
	if err != nil || created.AgentExecutionProfile != domain.AgentExecutionProfileTrustedLocal {
		t.Fatalf("Trusted Local Task=%#v err=%v", created, err)
	}
	accepted := submitAndAcceptRealTask(t, ctx, service, created, "trusted-local")
	if accepted.Charter.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileTrustedLocal || accepted.Charter.SandboxMode() != "trusted-host" {
		t.Fatalf("Trusted Local Charter binding=%#v sandbox=%q", accepted.Charter.AgentExecutionProfileBinding(), accepted.Charter.SandboxMode())
	}
}

func TestProfileSwitchChangesOnlySuccessorAndOrdinaryRetryInheritsCurrentProfile(t *testing.T) {
	ctx := context.Background()
	service, db, raw, room := realSpecCodingAppFixture(t, ctx)
	created, err := service.CreateTask(ctx, realSpecCodingCreateRequest(room))
	if err != nil {
		t.Fatal(err)
	}
	accepted := submitAndAcceptRealTask(t, ctx, service, created, "profile-switch")
	envelope, err := speccoding.LoadInstalledEnvelope(room.repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	service = app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{&fakeAdapter{id: "pi"}}, Supervisor: &fakeSupervisor{},
		Presence: fakePresence{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)},
		IDs: app.RandomIDs{}, InstalledSpecCodingEnvelope: &envelope,
	})
	runResult, err := service.CreateRun(ctx, app.CreateRunRequest{
		CommandMeta: meta("create-profile-switch-run", "create-profile-switch-run"), TaskID: created.Task.ID(), RevisionID: accepted.Review.RevisionID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepareRequest := app.PrepareRunRequest{CommandMeta: meta("prepare-profile-switch", "prepare-profile-switch"), RunID: runResult.Run.ID(), ExpectedVersion: runResult.Run.Version()}
	prepared, err := service.PrepareRun(ctx, prepareRequest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.PrepareRun(ctx, prepareRequest)
	if err != nil || !replay.Replayed || replay.Attempt.ID() != prepared.Attempt.ID() || replay.Attempt.AgentExecutionProfileBinding() != prepared.Attempt.AgentExecutionProfileBinding() {
		t.Fatalf("prepare replay=%#v err=%v", replay, err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET state='revision_required' WHERE id=?`, runResult.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	switchRequest := app.PrepareProfileSwitchRequest{
		CommandMeta: meta("switch-profile-minimal", "switch-profile-minimal"), RunID: runResult.Run.ID(),
		ExpectedVersion: prepared.Run.Version(), Profile: domain.AgentExecutionProfileMinimal, Reason: "reduce the successor capability boundary",
	}
	switched, err := service.PrepareProfileSwitch(ctx, switchRequest)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Reader().ListRunEvents(ctx, runResult.Run.ID())
	if err != nil {
		t.Fatal(err)
	}
	reset := false
	for _, event := range events {
		reset = reset || event.Type() == "automatic_retry.reset"
	}
	if !reset {
		t.Fatal("human profile successor did not reset the automatic retry budget")
	}
	switchReplay, err := service.PrepareProfileSwitch(ctx, switchRequest)
	if err != nil || !switchReplay.Replayed || switchReplay.Attempt.ID() != switched.Attempt.ID() || switchReplay.Attempt.AgentExecutionProfileBinding() != switched.Attempt.AgentExecutionProfileBinding() {
		t.Fatalf("switch replay=%#v err=%v", switchReplay, err)
	}
	oldAttempt, err := db.Reader().GetAttempt(ctx, prepared.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	charter, err := db.Reader().GetCharter(ctx, accepted.Charter.ID())
	if err != nil {
		t.Fatal(err)
	}
	if oldAttempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileStandard || charter.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileStandard || switched.Attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileMinimal {
		t.Fatalf("historical Charter/Attempt mutated: charter=%q old=%q successor=%q", charter.AgentExecutionProfileBinding().Profile(), oldAttempt.AgentExecutionProfileBinding().Profile(), switched.Attempt.AgentExecutionProfileBinding().Profile())
	}
	preference, err := db.Reader().GetTaskAgentExecutionProfilePreference(ctx, created.Task.ID())
	if err != nil || preference.Version() != 2 || preference.Profile() != domain.AgentExecutionProfileMinimal {
		t.Fatalf("current preference=%#v err=%v", preference, err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET state='revision_required' WHERE id=?`, runResult.Run.ID().String()); err != nil {
		t.Fatal(err)
	}
	retry, err := service.PrepareRetry(ctx, app.PrepareRetryRequest{
		CommandMeta: meta("retry-current-profile", "retry-current-profile"), RunID: runResult.Run.ID(), ExpectedVersion: switched.Run.Version(),
		Reason: "ordinary retry", Instructions: "inherit the current profile exactly",
	})
	if err != nil || retry.Attempt.Sequence() != 3 || retry.Attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileMinimal {
		t.Fatalf("ordinary Retry=%#v err=%v", retry, err)
	}
}
