package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

// Storage-only fixture: business admission is separately enforced by app. This
// exercises durable immutable launch/revocation, not runtime qualification.
func TestRestrictedLaunchImmutableRevocationSurvivesSQLiteReopen(t *testing.T) {
	f := newLocalReviewFixture(t)
	ctx := context.Background()
	d := strings.Repeat("a", 64)
	p, _, _ := insertExecutionSettingsProject(t, ctx, f.db, "restricted storage", f.seeded.now)
	w := domain.WorkflowExecution{ID: "restricted-storage-only", TaskID: f.seeded.task.ID(), ProjectID: p.ID(), ActorID: "owner", SessionID: "session", Version: 1, Status: "coding", InputDigest: d, AuthorityDigest: d, RevisionID: domain.NewTechnicalPlanRevisionID(), RunID: f.run.ID(), Configuration: []byte(`{}`), CreatedAt: f.seeded.now}
	now := time.Now().UTC()
	b := domain.RestrictedLaunch{Schema: "chora.restricted-launch.v2", Authorization: "auth", Workflow: w.ID, Project: p.ID().String(), Task: w.TaskID.String(), Charter: f.run.CharterID().String(), Run: w.RunID.String(), Attempt: f.attempt.ID().String(), Actor: "owner", Gate: "gate", Operation: "op", InputDigest: d, AuthorityDigest: d, LaunchToken: "launch", Channel: "channel", SourceDigest: d, ConfigDigest: d, Epoch: 1, AuthorizedAt: now, ObservedAt: now, Deadline: now.Add(time.Minute)}
	err := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertWorkflow(ctx, w); err != nil {
			return err
		}
		return tx.InsertRestrictedLaunch(ctx, b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRestrictedLaunch(ctx, b) }); err == nil {
		t.Fatal("duplicate channel accepted")
	}
	raw, err := sql.Open("sqlite", f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE workflow_pi_launches SET binding='{}'`, `DELETE FROM workflow_pi_launches`} {
		if _, err = raw.Exec(query); err == nil {
			t.Fatal("immutable launch altered", query)
		}
	}
	if err = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.RevokeRestrictedChannels(ctx) }); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE workflow_pi_launches SET revoked=0`); err == nil {
		t.Fatal("revoked channel resurrected")
	}
	_ = raw.Close()
	_ = f.db.Close()
	reopened, err := sqlite.Open(ctx, f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, revoked, err := reopened.Reader().GetRestrictedLaunch(ctx, b.Channel)
	if err != nil || !revoked || got != b {
		t.Fatalf("recovered=%+v revoked=%v err=%v", got, revoked, err)
	}
}
