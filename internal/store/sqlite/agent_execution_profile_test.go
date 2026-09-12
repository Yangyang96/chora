package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestAgentExecutionProfileBindingRoundTripsForCharterAndAttempt(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	ctx := context.Background()
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "profile round-trip", "")
	task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "Pi Task", "persist binding", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{seeded.revision.RevisionID()},
		WorkspaceRoot: seeded.room.WorkspaceRoot(), AdapterID: "pi", SandboxMode: "trusted-host", ExpectedOutput: "patch", ResponsibleHuman: "owner",
		CapabilityEnvelope: domain.CapabilityEnvelope{"native": true}, AgentExecutionProfileBinding: binding, Initiator: "test", CreatedAt: seeded.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	run, err = run.Transition(domain.CommandPrepareRun, seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var attempt domain.Attempt
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		snapshot, err := contextcore.NewAssembler(tx).Assemble(ctx, contextcore.AssembleRequest{SnapshotID: domain.NewContextSnapshotID(), Task: task, Charter: charter})
		if err != nil {
			return err
		}
		attempt, err = domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "pi", AgentExecutionProfileBinding: binding, CreatedAt: seeded.now.Add(time.Second)})
		if err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		return tx.InsertAttempt(ctx, attempt)
	})
	if err != nil {
		t.Fatal(err)
	}
	gotCharter, err := db.Reader().GetCharter(ctx, charter.ID())
	if err != nil || gotCharter.AgentExecutionProfileBinding() != binding {
		t.Fatalf("Charter binding=%#v, %v", gotCharter.AgentExecutionProfileBinding(), err)
	}
	gotAttempt, err := db.Reader().GetAttempt(ctx, attempt.ID())
	if err != nil || gotAttempt.AgentExecutionProfileBinding() != binding {
		t.Fatalf("Attempt binding=%#v, %v", gotAttempt.AgentExecutionProfileBinding(), err)
	}

	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `UPDATE attempts SET agent_capability_policy='chora.standard.v1' WHERE id=?`, attempt.ID().String()); err == nil {
		t.Fatal("persisted Attempt binding became mutable")
	}
}

func TestIsolatedProfileSQLConstraintAcceptsOnlyExactBindingTuple(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	ctx := context.Background()
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	insert := `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	values := []any{domain.NewCharterID().String(), seeded.task.ID().String(), "Goal", seeded.room.WorkspaceRoot(), "pi", "colima-docker", "patch", "owner", "test", seeded.now.Format(time.RFC3339Nano), "isolated_local", "public_pi_image", "docker", "chora.isolated-local.v1", ""}
	if _, err = raw.ExecContext(ctx, insert, values...); err != nil {
		t.Fatalf("exact Isolated Local tuple rejected: %v", err)
	}
	for index, invalid := range []struct {
		column int
		value  string
	}{{11, "local_pi"}, {12, "trusted_host"}, {13, "pi.native"}, {14, "chora.trusted-local-disclosure.v1"}} {
		candidate := append([]any(nil), values...)
		candidate[0] = domain.NewCharterID().String()
		candidate[invalid.column] = invalid.value
		if _, err = raw.ExecContext(ctx, insert, candidate...); err == nil {
			t.Fatalf("invalid Isolated Local tuple %d was accepted", index)
		}
	}
	if _, err = raw.ExecContext(ctx, `UPDATE run_charters SET agent_capability_policy='chora.standard.v1' WHERE id=?`, values[0]); err == nil {
		t.Fatal("persisted Isolated Local binding became mutable")
	}
}
