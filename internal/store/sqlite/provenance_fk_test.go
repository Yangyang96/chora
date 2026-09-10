package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestOperationalRecordsRejectAttemptFromDifferentRun(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "codex", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "other", "")
	task, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "other", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{seeded.revision.RevisionID()}, WorkspaceRoot: seeded.room.WorkspaceRoot(), AdapterID: "codex", SandboxMode: "workspace-write", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		return tx.InsertRun(ctx, otherRun)
	}); err != nil {
		t.Fatal(err)
	}
	observationID := domain.NewObservationID()
	artifactID := domain.NewArtifactID()
	checkID := domain.NewCheckID()
	interventionID := domain.NewInterventionID()
	handoffID := domain.NewHandoffID()
	tests := []struct {
		name, table, id string
		insert          func(storecontract.WriteTx) error
	}{
		{"observation", "observations", observationID.String(), func(tx storecontract.WriteTx) error {
			return tx.InsertObservation(ctx, storecontract.Observation{ID: observationID, RunID: otherRun.ID(), AttemptID: ptrAttempt(attempt.ID()), Kind: "note", Body: "x", CreatedAt: seeded.now})
		}},
		{"artifact", "artifacts", artifactID.String(), func(tx storecontract.WriteTx) error {
			return tx.InsertArtifact(ctx, storecontract.Artifact{ID: artifactID, RunID: otherRun.ID(), AttemptID: ptrAttempt(attempt.ID()), Kind: "patch", Locator: "file:///x", CreatedAt: seeded.now})
		}},
		{"check", "checks", checkID.String(), func(tx storecontract.WriteTx) error {
			return tx.InsertCheck(ctx, storecontract.Check{ID: checkID, RunID: otherRun.ID(), AttemptID: ptrAttempt(attempt.ID()), Name: "test", Status: "passed", CreatedAt: seeded.now})
		}},
		{"intervention", "interventions", interventionID.String(), func(tx storecontract.WriteTx) error {
			return tx.InsertIntervention(ctx, storecontract.Intervention{ID: interventionID, RunID: otherRun.ID(), AttemptID: ptrAttempt(attempt.ID()), Kind: "human", Reason: "x", RequestedBy: "owner", CreatedAt: seeded.now})
		}},
		{"handoff", "handoffs", handoffID.String(), func(tx storecontract.WriteTx) error {
			return tx.InsertHandoff(ctx, storecontract.Handoff{ID: handoffID, RunID: otherRun.ID(), AttemptID: ptrAttempt(attempt.ID()), FromActor: "a", ToActor: "b", Reason: "x", Status: "requested", CreatedAt: seeded.now})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tc.insert(tx) })
			if err == nil {
				t.Fatal("cross-run attempt accepted")
			}
			raw, err := sql.Open("sqlite", seeded.path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var count int
			if err := raw.QueryRow("SELECT count(*) FROM "+tc.table+" WHERE id=?", tc.id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("partial rows=%d", count)
			}
		})
	}
}

func ptrAttempt(id domain.AttemptID) *domain.AttemptID { return &id }
