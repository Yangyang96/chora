package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestDecisionGateResolutionAndHistorySurviveReopen(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	snapshot := assembleSeedSnapshot(t, db, seeded)
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: "fake", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := domain.NewExecutionDecisionGate(domain.ExecutionDecisionGateParams{
		ID: domain.NewDecisionGateID(), RunID: seeded.run.ID(), AttemptID: attempt.ID(), Question: "Continue?", Context: "Use the frozen context.",
		Options:        []domain.DecisionGateOption{{ID: "continue", Label: "Continue", Impact: "Finish."}, {ID: "stop", Label: "Stop", Impact: "Return."}},
		Recommendation: "Continue.", Impact: "Controls Fake execution.", RequestedAt: seeded.now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		return tx.InsertDecisionGate(ctx, gate)
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	_, directErr := raw.ExecContext(ctx, `UPDATE execution_decision_gates SET status='resolved',selected_option_id='unknown',note='',actor_id='owner',session_id='browser',resolved_at=? WHERE id=?`, seeded.now.Add(2*time.Second).Format(time.RFC3339Nano), gate.ID().String())
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if directErr == nil || !strings.Contains(directErr.Error(), "selected option does not exist") {
		t.Fatalf("direct unknown-option resolution error = %v", directErr)
	}
	resolved, err := gate.Resolve("continue", "Proceed with selected evidence.", "owner", "browser", seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.ResolveDecisionGateCAS(ctx, gate, resolved) }); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.ResolveDecisionGateCAS(ctx, gate, resolved) }); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("second resolution error = %v", err)
	}
	path := seeded.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Reader().GetLatestDecisionGateForRun(ctx, seeded.run.ID())
	if err != nil || loaded.Status() != domain.DecisionGateResolved || loaded.SelectedOptionID() != "continue" || loaded.Note() != "Proceed with selected evidence." {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	history, err := reopened.Reader().ListDecisionGatesForRun(ctx, seeded.run.ID())
	if err != nil || len(history) != 1 || history[0].ID() != gate.ID() {
		t.Fatalf("history=%#v err=%v", history, err)
	}
}
