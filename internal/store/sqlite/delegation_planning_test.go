package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"reflect"
	"testing"
	"time"
)

func TestPlanningAuthorizationBindsOneAttemptAndSurvivesReopen(t *testing.T) {
	f := newVerificationFixture(t)
	ctx := context.Background()
	now := f.seeded.now
	p := domain.DelegationPlanningIntent{ParentTaskID: f.seeded.task.ID(), RunID: f.seeded.run.ID(), Version: 1, State: domain.DelegationPlanningRunning, AuthorityDigest: sha256.Sum256([]byte("frozen authority")), ActorID: "human", SessionID: "browser", CreatedAt: now, UpdatedAt: now}
	if err := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertDelegationPlanning(ctx, p) }); err != nil {
		t.Fatal(err)
	}
	bound, err := p.BindAttempt(f.agentAttempt.ID(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDelegationPlanningCAS(ctx, p.Version, bound) }); err != nil {
		t.Fatal(err)
	}
	if _, err = bound.BindAttempt(domain.NewAttemptID(), now.Add(2*time.Second)); err == nil {
		t.Fatal("authorization rebound")
	}
	raw, err := sql.Open("sqlite", f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, query := range []string{`UPDATE delegation_planning_intents SET authority_digest=zeroblob(32)`, `UPDATE delegation_planning_intents SET actor_id='another'`, `UPDATE delegation_planning_intents SET attempt_id=NULL`, `UPDATE delegation_planning_intents SET max_planning_attempts=2`, `UPDATE delegation_planning_intents SET max_assignments=5`, `UPDATE delegation_planning_intents SET proposal_schema='future'`, `DELETE FROM delegation_planning_intents`} {
		if _, err = raw.Exec(query); err == nil {
			t.Fatalf("frozen authority changed: %s", query)
		}
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Reader().GetDelegationPlanning(ctx, p.ParentTaskID)
	if err != nil || !reflect.DeepEqual(got, bound) {
		t.Fatalf("reopened=%#v err=%v", got, err)
	}
}
