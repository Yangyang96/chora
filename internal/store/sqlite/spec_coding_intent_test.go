package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestSpecCodingIntentIsImmutableAndRoomGated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(root, "chora.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", Description: "real task room", WorkspaceRoot: root, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Reviewable result", "The declared command proves the result.")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "real task", "bounded change", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	otherCriterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Archived Room blocks intent", "The existing Task cannot gain an intent after archive.")
	if err != nil {
		t.Fatal(err)
	}
	other, err := domain.NewTask(domain.NewTaskID(), room.ID(), "other", "blocked", []domain.AcceptanceCriterion{otherCriterion})
	if err != nil {
		t.Fatal(err)
	}
	intent := storecontract.SpecCodingIntent{
		TaskID:       task.ID(),
		IntentDigest: [32]byte{1}, IntentJSON: []byte(`{"schema":"intent"}`), CreatedAt: now,
	}
	intent.IntentDigest = sha256.Sum256(intent.IntentJSON)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, other); err != nil {
			return err
		}
		return tx.InsertSpecCodingIntent(ctx, intent)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Reader().GetSpecCodingIntent(ctx, task.ID())
	if err != nil || loaded.TaskID != task.ID() || loaded.IntentDigest != intent.IntentDigest || !bytes.Equal(loaded.IntentJSON, intent.IntentJSON) || !loaded.CreatedAt.Equal(intent.CreatedAt) {
		t.Fatalf("loaded intent = %#v, err = %v", loaded, err)
	}
	loaded.IntentJSON[0] = '['
	reloaded, err := db.Reader().GetSpecCodingIntent(ctx, task.ID())
	if err != nil || !bytes.Equal(reloaded.IntentJSON, intent.IntentJSON) {
		t.Fatalf("stored canonical JSON was aliased: %#v, err = %v", reloaded, err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertSpecCodingIntent(ctx, intent) }); !errors.Is(err, storecontract.ErrSpecCodingConflict) {
		t.Fatalf("duplicate intent error = %v", err)
	}
	reusedDigest := intent
	reusedDigest.TaskID = other.ID()
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertSpecCodingIntent(ctx, reusedDigest) }); !errors.Is(err, storecontract.ErrSpecCodingConflict) {
		t.Fatalf("reused digest error = %v", err)
	}
	digestMismatch := intent
	digestMismatch.TaskID = other.ID()
	digestMismatch.IntentJSON = []byte(`{"schema":"different"}`)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertSpecCodingIntent(ctx, digestMismatch) }); !errors.Is(err, storecontract.ErrSpecCodingConflict) {
		t.Fatalf("mismatched digest error = %v", err)
	}
	invalidCriterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Invalid intent is rejected", "Only canonical, digest-bound JSON is stored.")
	if err != nil {
		t.Fatal(err)
	}
	invalidTask, err := domain.NewTask(domain.NewTaskID(), room.ID(), "invalid intent", "must roll back", []domain.AcceptanceCriterion{invalidCriterion})
	if err != nil {
		t.Fatal(err)
	}
	invalid := intent
	invalid.TaskID = invalidTask.ID()
	invalid.IntentJSON = []byte(`{"schema":`)
	invalid.IntentDigest = sha256.Sum256(invalid.IntentJSON)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, invalidTask); err != nil {
			return err
		}
		return tx.InsertSpecCodingIntent(ctx, invalid)
	}); !errors.Is(err, storecontract.ErrSpecCodingConflict) {
		t.Fatalf("invalid JSON error = %v", err)
	}
	if _, err := db.Reader().GetTask(ctx, invalidTask.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("invalid intent transaction was not atomic: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE user_spec_coding_intents SET intent_json=json('{}') WHERE task_id=?`, task.ID().String()); err == nil {
		t.Fatal("intent update unexpectedly succeeded")
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM user_spec_coding_intents WHERE task_id=?`, task.ID().String()); err == nil {
		t.Fatal("intent delete unexpectedly succeeded")
	}

	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		current, err := tx.GetRoom(ctx, room.ID())
		if err != nil {
			return err
		}
		archived, err := current.Archive(current.Version(), now.Add(time.Minute))
		if err != nil {
			return err
		}
		event := storecontract.RoomLifecycleEvent{RoomID: room.ID(), FromState: current.State(), ToState: archived.State(), Version: archived.Version(), ActorID: "actor", SessionID: "session", IdempotencyKeyHash: [32]byte{1}, RequestDigest: [32]byte{2}, OccurredAt: archived.UpdatedAt()}
		return tx.SaveRoomLifecycleCAS(ctx, current.Version(), archived, event)
	}); err != nil {
		t.Fatal(err)
	}
	blocked := intent
	blocked.TaskID = other.ID()
	blocked.IntentJSON = []byte(`{"schema":"blocked"}`)
	blocked.IntentDigest = sha256.Sum256(blocked.IntentJSON)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertSpecCodingIntent(ctx, blocked) }); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archived Room error = %v", err)
	}
	if _, err := db.Reader().GetSpecCodingIntent(ctx, other.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("partial intent persisted: %v", err)
	}
}

func TestSpecCodingIntentRestoreRejectsDigestDrift(t *testing.T) {
	ctx := context.Background()
	db, task, intent := seedSpecCodingIntent(t, ctx)
	defer db.Close()
	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER user_spec_coding_intents_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE user_spec_coding_intents SET intent_digest=zeroblob(32) WHERE task_id=?`, task.ID().String()); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Reader().GetSpecCodingIntent(ctx, task.ID())
	if !errors.Is(err, storecontract.ErrSpecCodingConflict) || loaded.TaskID.Valid() || loaded.IntentDigest != ([32]byte{}) || loaded.IntentJSON != nil || !loaded.CreatedAt.IsZero() {
		t.Fatalf("digest-drift restore = %#v, err = %v; original=%x", loaded, err, intent.IntentDigest)
	}
}

func TestV17UpgradeAddsImmutableSpecCodingIntentTable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(root, "v17.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 41 {
		t.Fatalf("migration count = %d", len(available))
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:17] {
		if _, err := raw.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := raw.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	roomID, taskID := domain.NewRoomID(), domain.NewTaskID()
	if _, err := raw.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "upgrade room", "", root, timeText(now), timeText(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "existing task", "preserve", "open", timeText(now), timeText(now)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, raw); err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"schema":"upgrade-intent"}`)
	digest := sha256.Sum256(canonical)
	if _, err := raw.ExecContext(ctx, `INSERT INTO user_spec_coding_intents(task_id,intent_digest,intent_json,created_at) VALUES(?,?,?,?)`, taskID.String(), digest[:], canonical, timeText(now)); err != nil {
		t.Fatal(err)
	}
	var version, taskCount int
	if err := raw.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id=?`, taskID.String()).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if version != 41 || taskCount != 1 {
		t.Fatalf("version=%d preserved tasks=%d", version, taskCount)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE user_spec_coding_intents SET created_at=? WHERE task_id=?`, timeText(now.Add(time.Second)), taskID.String()); err == nil {
		t.Fatal("upgraded intent update unexpectedly succeeded")
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM user_spec_coding_intents WHERE task_id=?`, taskID.String()); err == nil {
		t.Fatal("upgraded intent delete unexpectedly succeeded")
	}
}

func seedSpecCodingIntent(t *testing.T, ctx context.Context) (*Store, domain.Task, storecontract.SpecCodingIntent) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(root, "chora.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", Description: "real task room", WorkspaceRoot: root, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "Reviewable result", "The declared command proves the result.")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "real task", "bounded change", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	intent := storecontract.SpecCodingIntent{TaskID: task.ID(), IntentJSON: []byte(`{"schema":"intent"}`), CreatedAt: now}
	intent.IntentDigest = sha256.Sum256(intent.IntentJSON)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		return tx.InsertSpecCodingIntent(ctx, intent)
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, task, intent
}
