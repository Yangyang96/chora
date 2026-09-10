package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestTaskAgentExecutionProfilesAndTrustedAcknowledgementsRoundTripAfterRestart(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	now := seeded.now.Add(time.Minute)
	tasks := make(map[domain.AgentExecutionProfile]domain.Task)
	for _, profile := range domain.SupportedAgentExecutionProfiles() {
		criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "profile "+string(profile), "")
		task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "Task "+string(profile), "persist profile", []domain.AcceptanceCriterion{criterion})
		if err != nil {
			t.Fatal(err)
		}
		preference, err := domain.NewAgentExecutionProfilePreference(domain.AgentExecutionProfilePreferenceRecord{
			TaskID: task.ID(), Version: 1, Profile: profile, ActorID: "owner", SessionID: "session", SelectedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			if err := tx.InsertTask(ctx, task); err != nil {
				return err
			}
			return tx.InsertTaskAgentExecutionProfilePreference(ctx, preference)
		}); err != nil {
			t.Fatal(err)
		}
		tasks[profile] = task
	}
	for _, version := range []string{"chora.trusted-local-disclosure.v1", "chora.trusted-local-disclosure.v2"} {
		acknowledgement, err := domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
			PolicyVersion: version, ActorID: "owner", SessionID: "session-" + version, AcknowledgedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			return tx.InsertTrustedLocalAcknowledgement(ctx, acknowledgement)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for profile, task := range tasks {
		preference, err := db.Reader().GetTaskAgentExecutionProfilePreference(ctx, task.ID())
		if err != nil || preference.Profile() != profile || preference.Version() != 1 || preference.ActorID() != "owner" {
			t.Fatalf("profile %q preference=%#v err=%v", profile, preference, err)
		}
	}
	for _, version := range []string{"chora.trusted-local-disclosure.v1", "chora.trusted-local-disclosure.v2"} {
		acknowledgement, err := db.Reader().GetTrustedLocalAcknowledgement(ctx, "owner", version)
		if err != nil || acknowledgement.PolicyVersion() != version || acknowledgement.ActorID() != "owner" {
			t.Fatalf("policy %q acknowledgement=%#v err=%v", version, acknowledgement, err)
		}
	}
	if _, err := db.Reader().GetTrustedLocalAcknowledgement(ctx, "owner", "chora.trusted-local-disclosure.v3"); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("unacknowledged policy error=%v", err)
	}

	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `UPDATE agent_execution_profile_preferences SET profile='minimal' WHERE task_id=?`, tasks[domain.AgentExecutionProfileStandard].ID().String()); err == nil {
		t.Fatal("Task Agent execution profile preference became mutable")
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM trusted_local_acknowledgements WHERE actor_id='owner'`); err == nil {
		t.Fatal("Trusted Local acknowledgement became mutable")
	}
}

func TestTaskAgentExecutionProfilePreferenceRequiresExactSuccessorVersion(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	preference, err := domain.NewAgentExecutionProfilePreference(domain.AgentExecutionProfilePreferenceRecord{
		TaskID: seeded.task.ID(), Version: 2, Profile: domain.AgentExecutionProfileMinimal,
		ActorID: "owner", SessionID: "session", SelectedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.InsertTaskAgentExecutionProfilePreference(ctx, preference)
	}); !errors.Is(err, storecontract.ErrExecutionProfileConflict) {
		t.Fatalf("out-of-order preference error=%v", err)
	}
}
