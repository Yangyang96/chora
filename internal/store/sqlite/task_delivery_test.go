package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/migrations"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestTaskDeliveryIntentCASAndOutcomeSurviveDatabaseReopen(t *testing.T) {
	f := newLocalReviewFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: "delivery fixture", Checkout: filepath.Join(t.TempDir(), "repo"), CommonGitDir: filepath.Join(t.TempDir(), "git"), PhysicalIdentity: "1111111111111111111111111111111111111111111111111111111111111111", IdentitySource: "inspected", CreatedAt: now}
	o := storecontract.DeliveryOperation{ID: "delivery-test", TaskID: f.run.TaskID(), RunID: f.run.ID(), RepositoryID: repo.ID, Kind: "commit", State: "preview", Version: 1, RequestDigest: sha256.Sum256([]byte("request")), PreviewJSON: []byte(`{"ResultDigest":"immutable"}`), OutcomeJSON: []byte(`{}`), CreatedAt: now, UpdatedAt: now}
	e := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if e := tx.InsertRepository(ctx, repo); e != nil {
			return e
		}
		return tx.SaveDeliveryOperation(ctx, 0, o)
	})
	if e != nil {
		t.Fatal(e)
	}
	next := o
	next.State = "writing"
	next.Version = 2
	next.UpdatedAt = now.Add(time.Second)
	e = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 1, next) })
	if e != nil {
		t.Fatal(e)
	}
	if e = f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 1, next) }); !errors.Is(e, storecontract.ErrVersionConflict) {
		t.Fatalf("stale CAS=%v", e)
	}
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := sqlite.Open(ctx, f.seeded.path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	pending, e := reopened.Reader().GetDeliveryOperation(ctx, o.ID)
	if e != nil || pending.State != "writing" || string(pending.PreviewJSON) != string(o.PreviewJSON) {
		t.Fatalf("lost pending intent %#v %v", pending, e)
	}
	bad := pending
	bad.Version++
	bad.State = "preview"
	if e = reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, pending.Version, bad) }); !errors.Is(e, storecontract.ErrVersionConflict) {
		t.Fatalf("rearmed intent: %v", e)
	}
	bad.State = "succeeded"
	bad.PreviewJSON = []byte(`{"ResultDigest":"different"}`)
	if e = reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, pending.Version, bad) }); e == nil {
		t.Fatal("rewrote immutable intent")
	}
	done := pending
	done.State = "succeeded"
	done.Version++
	done.OutcomeJSON = []byte(`{"Commit":"proven"}`)
	done.UpdatedAt = now.Add(2 * time.Second)
	if e = reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, pending.Version, done) }); e != nil {
		t.Fatal(e)
	}
	bad = done
	bad.Version++
	bad.OutcomeJSON = []byte(`{"Commit":"rewritten"}`)
	if e = reopened.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, done.Version, bad) }); !errors.Is(e, storecontract.ErrVersionConflict) {
		t.Fatalf("rewrote terminal outcome: %v", e)
	}
	if e = reopened.Close(); e != nil {
		t.Fatal(e)
	}
	again, e := sqlite.Open(ctx, f.seeded.path)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	records, e := again.Reader().ListDeliveryOperations(ctx, o.TaskID)
	if e != nil || len(records) != 1 || records[0].State != "succeeded" || string(records[0].OutcomeJSON) != string(done.OutcomeJSON) {
		t.Fatalf("outcome lost %#v %v", records, e)
	}
}

func TestTaskDeliveryPersistsFullReviewPolicyPatchEnvelope(t *testing.T) {
	f := newLocalReviewFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: "large delivery fixture", Checkout: filepath.Join(t.TempDir(), "repo"), CommonGitDir: filepath.Join(t.TempDir(), "git"), PhysicalIdentity: "2222222222222222222222222222222222222222222222222222222222222222", IdentitySource: "inspected", CreatedAt: now}
	// The immutable intent stores []byte as base64. Exercise the entire accepted
	// 100 MiB review boundary, including its 4/3 JSON envelope expansion.
	raw, err := json.Marshal(struct{ Patch []byte }{Patch: bytes.Repeat([]byte("x"), domain.TaskPatchBytes)})
	if err != nil {
		t.Fatal(err)
	}
	o := storecontract.DeliveryOperation{ID: "large-delivery", TaskID: f.run.TaskID(), RunID: f.run.ID(), RepositoryID: repo.ID, Kind: "commit", State: "preview", Version: 1, RequestDigest: sha256.Sum256(raw), PreviewJSON: raw, OutcomeJSON: []byte(`{}`), CreatedAt: now, UpdatedAt: now}
	if err := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRepository(ctx, repo); err != nil {
			return err
		}
		return tx.SaveDeliveryOperation(ctx, 0, o)
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := f.db.Reader().GetDeliveryOperation(ctx, o.ID)
	if err != nil || sha256.Sum256(saved.PreviewJSON) != o.RequestDigest {
		t.Fatalf("full-size envelope not durable: %v", err)
	}
}

func TestV38DeliveryCleanupUpgradePreservesPendingAndTerminalIntents(t *testing.T) {
	f := newLocalReviewFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: "upgrade", Checkout: filepath.Join(t.TempDir(), "repo"), CommonGitDir: filepath.Join(t.TempDir(), "git"), PhysicalIdentity: "3333333333333333333333333333333333333333333333333333333333333333", IdentitySource: "inspected", CreatedAt: now}
	o := storecontract.DeliveryOperation{ID: "upgrade", TaskID: f.run.TaskID(), RunID: f.run.ID(), RepositoryID: repo.ID, Kind: "pr", State: "preview", Version: 1, RequestDigest: sha256.Sum256([]byte("upgrade")), PreviewJSON: []byte(`{"marker":"immutable"}`), OutcomeJSON: []byte(`{}`), CreatedAt: now, UpdatedAt: now}
	if err := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRepository(ctx, repo); err != nil {
			return err
		}
		return tx.SaveDeliveryOperation(ctx, 0, o)
	}); err != nil {
		t.Fatal(err)
	}
	o.State = "writing"
	o.Version++
	if err := f.db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 1, o) }); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the schema38 delivery table and retained row as a populated upgrade input.
	old, err := migrations.Files.ReadFile("0038_task_delivery_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TABLE result_closures; CREATE TEMP TABLE retained AS SELECT * FROM task_delivery_operations; DROP TABLE task_delivery_operations;` + string(old) + `; INSERT INTO task_delivery_operations SELECT * FROM retained; DELETE FROM schema_migrations WHERE version>=39;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	upgraded, err := sqlite.Open(ctx, f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.Reader().GetDeliveryOperation(ctx, o.ID)
	if err != nil || got.State != "writing" || got.Version != o.Version || !bytes.Equal(got.PreviewJSON, o.PreviewJSON) {
		t.Fatalf("lost intent %#v %v", got, err)
	}
	done := got
	done.Version++
	done.State = "succeeded"
	done.OutcomeJSON = []byte(`{"PR":"recorded"}`)
	if err = upgraded.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, got.Version, done) }); err != nil {
		t.Fatal(err)
	}
	clean := o
	clean.ID = "cleanup-upgrade"
	clean.Kind = "cleanup"
	clean.State = "preview"
	clean.Version = 1
	if err = upgraded.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 0, clean) }); err != nil {
		t.Fatal(err)
	}
	// Direct writes still cannot alter terminal evidence or delete history.
	raw, err = sql.Open("sqlite", f.seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`UPDATE task_delivery_operations SET outcome_json='{}' WHERE operation_id='upgrade'`); err == nil {
		t.Fatal("terminal evidence rewritten")
	}
	if _, err = raw.Exec(`DELETE FROM task_delivery_operations WHERE operation_id='upgrade'`); err == nil {
		t.Fatal("history deleted")
	}
}
