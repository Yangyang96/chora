package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestPopulatedV6UpgradeRejectsInferredLegacyPlanningHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := root + "/v6-room.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:6] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	roomID := domain.NewRoomID()
	taskID := domain.NewTaskID()
	criterionID := domain.NewCriterionID()
	historicalTaskID := domain.NewTaskID()
	historicalCriterionID := domain.NewCriterionID()
	historicalEntryID := domain.NewContextEntryID()
	historicalRevisionID := domain.NewContextRevisionID()
	historicalCharterID := domain.NewCharterID()
	historicalRunID := domain.NewRunID()
	historicalSnapshotID := domain.NewContextSnapshotID()
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Legacy Room", "Legacy brief", t.TempDir(), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "Existing V6 Task", "Continue after migration", "open", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_criteria(criterion_id,task_id,position,title,description) VALUES(?,?,?,?,?)`, criterionID.String(), taskID.String(), 0, "starts", "The existing Task starts after migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, historicalTaskID.String(), roomID.String(), "Historical Task", "Remain unchanged", "closed", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_criteria(criterion_id,task_id,position,title,description) VALUES(?,?,?,?,?)`, historicalCriterionID.String(), historicalTaskID.String(), 0, "historical", "historical criterion"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO context_entries(id,room_id,kind,created_at) VALUES(?,?,?,?)`, historicalEntryID.String(), roomID.String(), "brief", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO context_revisions(id,entry_id,revision_number,title,body,locator,sensitive,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, historicalRevisionID.String(), historicalEntryID.String(), 1, "Historical brief", "Do not rewrite", "legacy://brief", 0, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, historicalCharterID.String(), historicalTaskID.String(), "Remain unchanged", "/legacy", "fake", "legacy", "legacy result", "owner", "legacy", now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO charter_criteria(charter_id,criterion_id,position,title,description) VALUES(?,?,?,?,?)`, historicalCharterID.String(), historicalCriterionID.String(), 0, "historical", "historical criterion"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO charter_context_selections(charter_id,revision_id,position) VALUES(?,?,0)`, historicalCharterID.String(), historicalRevisionID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at,terminal_at) VALUES(?,?,?,?,?,?,?,?,?)`, historicalRunID.String(), historicalTaskID.String(), historicalCharterID.String(), "accepted", 9, 1, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	historicalCanonical := []byte(`{"legacy":true}`)
	historicalDigest := sha256.Sum256(historicalCanonical)
	if _, err := db.ExecContext(ctx, `INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, historicalSnapshotID.String(), historicalDigest[:], historicalCanonical, []byte("legacy snapshot"), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO snapshot_inclusions(snapshot_id,revision_id,position) VALUES(?,?,0)`, historicalSnapshotID.String(), historicalRevisionID.String()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, storecontract.ErrLegacyPlanningData) || !strings.Contains(err.Error(), "new data directory") {
		t.Fatalf("legacy open err=%v", err)
	}
}

func TestUpgradeV4RuntimeSessionPreservesExternalReferenceAndLeavesLaunchTokenEmpty(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v4.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:4] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := sha256.Sum256([]byte("fingerprint"))
	if _, err := db.ExecContext(ctx, `INSERT INTO runtime_sessions(id,attempt_id,adapter_id,runtime_kind,external_reference,version,working_root,security_fingerprint,process_identity,state,created_at,updated_at) VALUES('session','attempt','fake','process','legacy-ref',0,'/tmp',?,'','starting',?,?)`, fingerprint[:], time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var external, launch string
	if err := db.QueryRowContext(ctx, `SELECT external_reference,launch_token FROM runtime_sessions WHERE id='session'`).Scan(&external, &launch); err != nil {
		t.Fatal(err)
	}
	if external != "legacy-ref" || launch != "" {
		t.Fatalf("external=%q launch=%q", external, launch)
	}
}

func TestPopulatedV10UpgradePreservesRunAndAdmitsVerificationStates(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v10.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:10] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	roomID, taskID, criterionID := domain.NewRoomID(), domain.NewTaskID(), domain.NewCriterionID()
	charterID, runID := domain.NewCharterID(), domain.NewRunID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Room", "brief", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "Task", "Goal", "open", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_criteria(criterion_id,task_id,position,title,description) VALUES(?,?,?,?,?)`, criterionID.String(), taskID.String(), 0, "criterion", "description"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, charterID.String(), taskID.String(), "Goal", "/workspace", "fake", "sandbox", "patch", "owner", "test", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO charter_criteria(charter_id,criterion_id,position,title,description) VALUES(?,?,?,?,?)`, charterID.String(), criterionID.String(), 0, "criterion", "description"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, runID.String(), taskID.String(), charterID.String(), "awaiting_review", 7, 1, now, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var state string
	var version int
	if err := db.QueryRowContext(ctx, `SELECT state,version FROM runs WHERE id=?`, runID.String()).Scan(&state, &version); err != nil || state != "awaiting_review" || version != 7 {
		t.Fatalf("preserved run=%q/%d err=%v", state, version, err)
	}
	for _, next := range []string{"awaiting_verification", "verifying", "awaiting_review", "revision_required", "verification_recovery_required"} {
		if _, err := db.ExecContext(ctx, `UPDATE runs SET state=? WHERE id=?`, next, runID.String()); err != nil {
			t.Fatalf("state %q rejected: %v", next, err)
		}
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v11 run-table rebuild left a foreign-key violation")
	}
}

func TestPopulatedV5UpgradeToV6PreservesRowsAndInitializesAgentProjectionFields(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v5.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:5] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	security := sha256.Sum256([]byte("security"))
	if _, err := db.ExecContext(ctx, `INSERT INTO runtime_sessions(id,attempt_id,adapter_id,runtime_kind,external_reference,version,working_root,security_fingerprint,process_identity,state,created_at,updated_at,launch_token) VALUES('session','attempt','fake','process','external-1',4,'/workspace',?,'pid:1','running',?,?,'launch-1')`, security[:], now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO artifacts(id,run_id,attempt_id,kind,locator,digest,media_type,created_at) VALUES('artifact','run','attempt','patch','legacy.diff',NULL,'text/x-diff',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO checks(id,run_id,attempt_id,name,status,evidence,created_at) VALUES('check','run','attempt','legacy check','pending','legacy evidence',?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var external, launch, runtimeVersion string
	var version int
	var runtimeFingerprint []byte
	if err := db.QueryRowContext(ctx, `SELECT external_reference,launch_token,version,runtime_version,runtime_fingerprint FROM runtime_sessions WHERE id='session'`).Scan(&external, &launch, &version, &runtimeVersion, &runtimeFingerprint); err != nil {
		t.Fatal(err)
	}
	if external != "external-1" || launch != "launch-1" || version != 4 || runtimeVersion != "" || runtimeFingerprint != nil {
		t.Fatalf("runtime external=%q launch=%q version=%d runtime_version=%q fp=%x", external, launch, version, runtimeVersion, runtimeFingerprint)
	}
	var kind, locator, mediaType, description, role string
	var position int
	var artifactSource sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT kind,locator,media_type,description,position,role,source_event_id FROM artifacts WHERE id='artifact'`).Scan(&kind, &locator, &mediaType, &description, &position, &role, &artifactSource); err != nil {
		t.Fatal(err)
	}
	if kind != "patch" || locator != "legacy.diff" || mediaType != "text/x-diff" || description != "" || position != 0 || role != "output" || artifactSource.Valid {
		t.Fatalf("artifact=%q %q %q %q %d %q source=%#v", kind, locator, mediaType, description, position, role, artifactSource)
	}
	var criterionID, name, status, evidence string
	var checkPosition int
	var checkSource sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT criterion_id,position,name,status,evidence,source_event_id FROM checks WHERE id='check'`).Scan(&criterionID, &checkPosition, &name, &status, &evidence, &checkSource); err != nil {
		t.Fatal(err)
	}
	if criterionID != "" || checkPosition != 0 || name != "legacy check" || status != "unknown" || evidence != "legacy evidence" || checkSource.Valid {
		t.Fatalf("check=%q %d %q %q %q source=%#v", criterionID, checkPosition, name, status, evidence, checkSource)
	}
}

func TestPopulatedV8UpgradeWithEmptyLegacyPlanningUpgradesCleanly(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v8.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:8] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	roomID := domain.NewRoomID()
	taskID := domain.NewTaskID()
	now := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Room", "Description", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "Task", "Goal", "open", now, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version, taskCount, draftCount int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id=?`, taskID.String()).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM technical_plan_drafts`).Scan(&draftCount); err != nil {
		t.Fatal(err)
	}
	if version != 41 || taskCount != 1 || draftCount != 0 {
		t.Fatalf("version=%d tasks=%d drafts=%d", version, taskCount, draftCount)
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('technical_plan_drafts') WHERE lower(name) LIKE '%credential%' OR lower(name) LIKE '%transcript%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("technical_plan_drafts contains forbidden credential/transcript column")
	}
}

func TestRoomLifecycleUpgradeInitializesRowsAndMakesEventsAuthoritative(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v16-room-lifecycle.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:16] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	roomID := domain.NewRoomID()
	createdAt := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Upgraded Room", "", t.TempDir(), createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var state string
	var version int
	var archivedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state,version,archived_at FROM rooms WHERE id=?`, roomID.String()).Scan(&state, &version, &archivedAt); err != nil {
		t.Fatal(err)
	}
	if state != "active" || version != 1 || archivedAt.Valid {
		t.Fatalf("upgraded lifecycle=%q/%d/%#v", state, version, archivedAt)
	}
	archiveAt := createdAt.Add(time.Minute).Format(time.RFC3339Nano)
	key := sha256.Sum256([]byte("upgrade-archive-key"))
	digest := sha256.Sum256([]byte("upgrade-archive-request"))
	if _, err := db.ExecContext(ctx, `INSERT INTO room_lifecycle_events(room_id,version,from_state,to_state,actor_id,session_id,idempotency_key_hash,request_digest,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, roomID.String(), 2, "active", "archived", "owner", "session", key[:], digest[:], archiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state,version,archived_at FROM rooms WHERE id=?`, roomID.String()).Scan(&state, &version, &archivedAt); err != nil || state != "archived" || version != 2 || archivedAt.String != archiveAt {
		t.Fatalf("event-applied lifecycle=%q/%d/%#v err=%v", state, version, archivedAt, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE rooms SET state='active',version=3,updated_at=?,archived_at=NULL WHERE id=?`, createdAt.Add(2*time.Minute).Format(time.RFC3339Nano), roomID.String()); err == nil {
		t.Fatal("direct lifecycle update unexpectedly succeeded without an event")
	}
	if _, err := db.ExecContext(ctx, `UPDATE room_lifecycle_events SET actor_id='forged' WHERE room_id=?`, roomID.String()); err == nil {
		t.Fatal("lifecycle event update unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM room_lifecycle_events WHERE room_id=?`, roomID.String()); err == nil {
		t.Fatal("lifecycle event delete unexpectedly succeeded")
	}
	restoreAt := createdAt.Add(3 * time.Minute).Format(time.RFC3339Nano)
	restoreKey := sha256.Sum256([]byte("upgrade-restore-key"))
	restoreDigest := sha256.Sum256([]byte("upgrade-restore-request"))
	if _, err := db.ExecContext(ctx, `INSERT INTO room_lifecycle_events(room_id,version,from_state,to_state,actor_id,session_id,idempotency_key_hash,request_digest,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, roomID.String(), 3, "archived", "active", "owner", "session", restoreKey[:], restoreDigest[:], restoreAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state,version,archived_at FROM rooms WHERE id=?`, roomID.String()).Scan(&state, &version, &archivedAt); err != nil || state != "active" || version != 3 || archivedAt.Valid {
		t.Fatalf("restored lifecycle=%q/%d/%#v err=%v", state, version, archivedAt, err)
	}
}

func TestV19UpgradeAllowsSharedInstalledRepositoryAndPreservesRoomAuthority(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v18-shared-repository.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 41 {
		t.Fatalf("migration count = %d, want 41", len(available))
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:18] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	sharedRoot := t.TempDir()
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	firstRoom, firstTask := domain.NewRoomID(), domain.NewTaskID()
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, firstRoom.String(), "First", "preserved", sharedRoot, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, firstTask.String(), firstRoom.String(), "Existing Task", "preserve child foreign key", "open", now, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	secondRoom := domain.NewRoomID()
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, secondRoom.String(), "Second", "same installed repository", sharedRoot, now, now); err != nil {
		t.Fatalf("second Room sharing installed repository: %v", err)
	}
	var rooms, tasks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE workspace_root=?`, sharedRoot).Scan(&rooms); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id=? AND room_id=?`, firstTask.String(), firstRoom.String()).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if rooms != 2 || tasks != 1 {
		t.Fatalf("shared Rooms/preserved Task = %d/%d, want 2/1", rooms, tasks)
	}
	if _, err := db.ExecContext(ctx, `UPDATE rooms SET workspace_root=?,version=version+1,updated_at=? WHERE id=?`, t.TempDir(), time.Now().UTC().Format(time.RFC3339Nano), firstRoom.String()); err == nil {
		t.Fatal("Room repository authority became mutable after v19 rebuild")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v19 Room rebuild left a foreign-key violation")
	}
}

func TestV31ProjectRoomMigrationClassifiesOnlyProvenOwnership(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v30-project-rooms.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:30] {
		if _, err = db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
	bound, boundArchived, boundRemoved, fake, unknown, localConnected := domain.NewRoomID(), domain.NewRoomID(), domain.NewRoomID(), domain.NewRoomID(), domain.NewRoomID(), domain.NewRoomID()
	for _, item := range []struct {
		id   domain.RoomID
		name string
	}{{bound, "Renamed Room"}, {boundArchived, "Archived Room"}, {fake, "Fake legacy"}, {unknown, "Unknown"}, {localConnected, "Damaged Local Connected"}} {
		if _, err = db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at) VALUES(?,?,?,?,'active',1,?,?)`, item.id.String(), item.name, "", t.TempDir(), now, now); err != nil {
			t.Fatal(err)
		}
	}
	archiveAt := time.Date(2026, 9, 7, 1, 3, 3, 0, time.UTC).Format(time.RFC3339Nano)
	archiveKey, archiveDigest := sha256.Sum256([]byte("archive-key")), sha256.Sum256([]byte("archive-request"))
	if _, err = db.ExecContext(ctx, `INSERT INTO room_lifecycle_events(room_id,version,from_state,to_state,actor_id,session_id,idempotency_key_hash,request_digest,occurred_at) VALUES(?,2,'active','archived','owner','session',?,?,?)`, boundArchived.String(), archiveKey[:], archiveDigest[:], archiveAt); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at) VALUES(?,?,?,?,'active',1,?,?)`, boundRemoved.String(), "Removed Room", "", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	insertBinding := func(roomID domain.RoomID, name string) {
		locator := t.TempDir()
		if _, insertErr := db.ExecContext(ctx, `INSERT INTO repository_bindings(room_id,name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at) VALUES(?,? ,?,'open','','0123456789abcdef0123456789abcdef01234567','sha:0123456789abcdef0123456789abcdef01234567:tree:89abcdef0123456789abcdef0123456789abcdef',?,0,'active',1,?,?)`, roomID.String(), name, locator, locator, now, now); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	insertBinding(bound, "Original Project Name")
	insertBinding(boundArchived, "Archived Room Project")
	insertBinding(boundRemoved, "Removed Project")
	if _, err = db.ExecContext(ctx, `UPDATE repository_bindings SET state='removed',version=2,updated_at=? WHERE room_id=?`, archiveAt, boundRemoved.String()); err != nil {
		t.Fatal(err)
	}
	boundTask, boundCharter, boundRun := domain.NewTaskID(), domain.NewCharterID(), domain.NewRunID()
	if _, err = db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)`, boundTask.String(), bound.String(), "Historical Task", "Preserve", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, boundCharter.String(), boundTask.String(), "Preserve", t.TempDir(), "fake", "workspace", "patch", "owner", "test", now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at) VALUES(?,?,?,'draft',0,0,?,?)`, boundRun.String(), boundTask.String(), boundCharter.String(), now, now); err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("historical-root"))
	locator := "repo-" + boundTask.String() + "-seed"
	if _, err = db.ExecContext(ctx, `INSERT INTO task_worktrees(task_id,repository_identity,pinned_base_revision,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at,pinned_base_tree,base_ref,start_policy) VALUES(?,?,?,?,?,'provisioning',1,'',?,?,NULL,'','','legacy_admitted_base')`, boundTask.String(), "repo", strings.Repeat("a", 40), locator, fingerprint[:], now, now); err != nil {
		t.Fatal(err)
	}
	var historicalBefore string
	if err = db.QueryRowContext(ctx, `SELECT task.id||'|'||task.room_id||'|'||run.id||'|'||run.charter_id||'|'||worktree.task_id||'|'||hex(worktree.configured_root_fingerprint)||'|'||worktree.relative_locator FROM tasks task JOIN runs run ON run.task_id=task.id JOIN task_worktrees worktree ON worktree.task_id=task.id WHERE task.id=?`, boundTask.String()).Scan(&historicalBefore); err != nil {
		t.Fatal(err)
	}
	taskID, charterID := domain.NewTaskID(), domain.NewCharterID()
	if _, err = db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,? ,?,'open',?,?)`, taskID.String(), fake.String(), "Task", "Goal", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, charterID.String(), taskID.String(), "Goal", t.TempDir(), "fake", "workspace", "patch", "owner", "test", now); err != nil {
		t.Fatal(err)
	}
	localTask, localCharter := domain.NewTaskID(), domain.NewCharterID()
	if _, err = db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)`, localTask.String(), localConnected.String(), "Task", "Goal", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, localCharter.String(), localTask.String(), "Goal", t.TempDir(), "pi", "none", "patch", "owner", "test", now, "trusted_local", "local_pi", "trusted_host", "pi.native", "chora.trusted-local-disclosure.v1"); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var historicalAfter string
	if err = db.QueryRowContext(ctx, `SELECT task.id||'|'||task.room_id||'|'||run.id||'|'||run.charter_id||'|'||worktree.task_id||'|'||hex(worktree.configured_root_fingerprint)||'|'||worktree.relative_locator FROM tasks task JOIN runs run ON run.task_id=task.id JOIN task_worktrees worktree ON worktree.task_id=task.id WHERE task.id=?`, boundTask.String()).Scan(&historicalAfter); err != nil {
		t.Fatal(err)
	}
	if historicalAfter != historicalBefore {
		t.Fatalf("historical authority changed: before=%q after=%q", historicalBefore, historicalAfter)
	}
	for _, item := range []struct {
		id      domain.RoomID
		kind    string
		project bool
	}{{bound, "project", true}, {boundArchived, "project", true}, {boundRemoved, "project", true}, {fake, "legacy_standalone", false}, {unknown, "unclassified", false}, {localConnected, "unclassified", false}} {
		var kind string
		var project sql.NullString
		if err = db.QueryRowContext(ctx, `SELECT ownership_kind,project_id FROM rooms WHERE id=?`, item.id.String()).Scan(&kind, &project); err != nil {
			t.Fatal(err)
		}
		if kind != item.kind || project.Valid != item.project {
			t.Fatalf("room %s ownership=%q project=%v", item.id, kind, project)
		}
	}
	var projectName string
	if err = db.QueryRowContext(ctx, `SELECT name FROM projects WHERE default_room_id=?`, bound.String()).Scan(&projectName); err != nil {
		t.Fatal(err)
	}
	if projectName != "Original Project Name" {
		t.Fatalf("project name=%q", projectName)
	}
	var archivedRoomState, removedProjectState string
	if err = db.QueryRowContext(ctx, `SELECT state FROM rooms WHERE id=?`, boundArchived.String()).Scan(&archivedRoomState); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT state FROM projects WHERE default_room_id=?`, boundRemoved.String()).Scan(&removedProjectState); err != nil {
		t.Fatal(err)
	}
	if archivedRoomState != "archived" || removedProjectState != "archived" {
		t.Fatalf("archived Room=%q removed Project=%q", archivedRoomState, removedProjectState)
	}
}

func TestV31ProjectRoomMigrationRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v30-project-rooms-retry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:30] {
		if migration.version == 19 || migration.version == 29 {
			err = applyParentTableRebuildMigration(ctx, db, migration)
		} else {
			err = applyMigration(ctx, db, migration)
		}
		if err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
	}
	broken := available[30]
	broken.sql += "\nSELECT * FROM p2a_forced_interruption;"
	if err = applyMigration(ctx, db, broken); err == nil {
		t.Fatal("interrupted migration unexpectedly committed")
	}
	var version int
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 30 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var tableCount int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&tableCount); err != nil || tableCount != 0 {
		t.Fatalf("partial Project table count=%d err=%v", tableCount, err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 41 {
		t.Fatalf("retry version=%d err=%v", version, err)
	}
}

func TestV22UpgradeAddsAndRelocatesImmutableTaskWorktreeBindings(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v22-task-worktrees.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 41 {
		t.Fatalf("migration count=%d", len(available))
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:22] {
		if migration.version == 19 {
			if err := applyParentTableRebuildMigration(ctx, db, migration); err != nil {
				t.Fatalf("apply %s: %v", migration.name, err)
			}
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	roomID, taskID := domain.NewRoomID(), domain.NewTaskID()
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "Existing Room", "preserve", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "Existing Task", "must not be inferred", "open", now, now); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, available[22]); err != nil {
		t.Fatalf("apply %s: %v", available[22].name, err)
	}
	rootFingerprint := sha256.Sum256([]byte("configured root"))
	legacyLocator := "worktrees/" + taskID.String() + "-upgrade"
	if _, err := db.ExecContext(ctx, `INSERT INTO task_worktrees(task_id,repository_identity,pinned_base_revision,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at) VALUES(?,?,?,?,?,'provisioning',1,'',?,?,NULL)`, taskID.String(), "chora", strings.Repeat("a", 40), legacyLocator, rootFingerprint[:], now, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version, taskCount, bindingCount int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id=?`, taskID.String()).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM task_worktrees`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if version != 41 || taskCount != 1 || bindingCount != 1 {
		t.Fatalf("version=%d tasks=%d bindings=%d", version, taskCount, bindingCount)
	}
	var locator string
	if err := db.QueryRowContext(ctx, `SELECT relative_locator FROM task_worktrees WHERE task_id=?`, taskID.String()).Scan(&locator); err != nil {
		t.Fatal(err)
	}
	if locator != "chora-"+taskID.String()+"-upgrade" {
		t.Fatalf("migrated locator=%q", locator)
	}
	var recoveryIndexSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='task_worktrees_recovery_candidates'`).Scan(&recoveryIndexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recoveryIndexSQL, "state IN ('provisioning','recovery_required')") {
		t.Fatalf("startup recovery index predicate=%q", recoveryIndexSQL)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM task_worktrees WHERE task_id=?`, taskID.String()); err == nil {
		t.Fatal("upgraded Task worktree binding was deletable")
	}
}

func TestV25UpgradeBindsOnlyHistoricalPiRowsToStandard(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v25-agent-profiles.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	available, err := loadMigrations()
	if err != nil || len(available) != 41 {
		t.Fatalf("migrations=%d, %v", len(available), err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:24] {
		if migration.version == 19 {
			if err := applyParentTableRebuildMigration(ctx, db, migration); err != nil {
				t.Fatalf("apply %s: %v", migration.name, err)
			}
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
	}
	now := time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	roomID := domain.NewRoomID()
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "profiles", "", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	type historical struct {
		adapter string
		task    domain.TaskID
		charter domain.CharterID
		run     domain.RunID
		attempt domain.AttemptID
	}
	rows := []historical{
		{adapter: "pi", task: domain.NewTaskID(), charter: domain.NewCharterID(), run: domain.NewRunID(), attempt: domain.NewAttemptID()},
		{adapter: "fake", task: domain.NewTaskID(), charter: domain.NewCharterID(), run: domain.NewRunID(), attempt: domain.NewAttemptID()},
	}
	for _, row := range rows {
		if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, row.task.String(), roomID.String(), row.adapter, "preserve", "open", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, row.charter.String(), row.task.String(), "preserve", "/legacy", row.adapter, "legacy", "patch", "owner", "legacy", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, row.run.String(), row.task.String(), row.charter.String(), "ready", 1, 1, now, now); err != nil {
			t.Fatal(err)
		}
		canonical := []byte(`{"adapter":"` + row.adapter + `"}`)
		digest := sha256.Sum256(canonical)
		snapshotID := domain.NewContextSnapshotID()
		if _, err := db.ExecContext(ctx, `INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, snapshotID.String(), digest[:], canonical, canonical, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,adapter_id,state,created_at) VALUES(?,?,?,?,?,?,?,?)`, row.attempt.String(), row.run.String(), 1, snapshotID.String(), digest[:], row.adapter, "created", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for _, query := range []string{
			`SELECT agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy FROM run_charters WHERE id=?`,
			`SELECT agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy FROM attempts WHERE id=?`,
		} {
			id := row.charter.String()
			if strings.Contains(query, "FROM attempts") {
				id = row.attempt.String()
			}
			var profile, source, provider, policy, disclosure string
			if err := db.QueryRowContext(ctx, query, id).Scan(&profile, &source, &provider, &policy, &disclosure); err != nil {
				t.Fatal(err)
			}
			if row.adapter == "pi" {
				if profile != "standard" || source != "managed_pi_image" || provider != "docker" || policy != "chora.standard.v1" || disclosure != "" {
					t.Fatalf("migrated Pi binding=%q/%q/%q/%q/%q", profile, source, provider, policy, disclosure)
				}
			} else if profile != "" || source != "" || provider != "" || policy != "" || disclosure != "" {
				t.Fatalf("non-Pi binding inferred=%q/%q/%q/%q/%q", profile, source, provider, policy, disclosure)
			}
		}
		var preference string
		preferenceErr := db.QueryRowContext(ctx, `SELECT profile FROM agent_execution_profile_preferences WHERE task_id=?`, row.task.String()).Scan(&preference)
		if row.adapter == "pi" {
			if preferenceErr != nil || preference != "standard" {
				t.Fatalf("migrated Pi Task preference=%q err=%v", preference, preferenceErr)
			}
		} else if !errors.Is(preferenceErr, sql.ErrNoRows) {
			t.Fatalf("non-Pi Task gained preference=%q err=%v", preference, preferenceErr)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET agent_execution_profile='minimal' WHERE id=?`, rows[0].attempt.String()); err == nil {
		t.Fatal("migrated Attempt profile binding became mutable")
	}
}

func TestV30UpgradeRetainsHistoricalTaskWorktreeBaseAsLegacyUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v29-task-base-authority.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil || len(available) != 41 {
		t.Fatalf("migrations=%d err=%v", len(available), err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL CHECK(length(checksum)=32), applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:29] {
		if migration.version == 19 {
			if err := applyParentTableRebuildMigration(ctx, db, migration); err != nil {
				t.Fatalf("apply %s: %v", migration.name, err)
			}
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	roomID, taskID := domain.NewRoomID(), domain.NewTaskID()
	if _, err := db.ExecContext(ctx, `INSERT INTO rooms(id,name,description,workspace_root,created_at,updated_at) VALUES(?,?,?,?,?,?)`, roomID.String(), "historical", "", t.TempDir(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, taskID.String(), roomID.String(), "historical", "retain exact binding", "open", now, now); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	fingerprint := sha256.Sum256([]byte("historical configured root"))
	locator := "chora-" + taskID.String() + "-historical"
	if _, err := db.ExecContext(ctx, `INSERT INTO task_worktrees(task_id,repository_identity,pinned_base_revision,relative_locator,configured_root_fingerprint,state,version,reason,created_at,updated_at,ready_at) VALUES(?,?,?,?,?,'provisioning',1,'',?,?,NULL)`, taskID.String(), "chora", revision, locator, fingerprint[:], now, now); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, available[29]); err != nil {
		t.Fatal(err)
	}
	var gotRevision, tree, baseRef, policy string
	if err := db.QueryRowContext(ctx, `SELECT pinned_base_revision,pinned_base_tree,base_ref,start_policy FROM task_worktrees WHERE task_id=?`, taskID.String()).Scan(&gotRevision, &tree, &baseRef, &policy); err != nil {
		t.Fatal(err)
	}
	if gotRevision != revision || tree != "" || baseRef != "" || policy != domain.TaskStartPolicyLegacy {
		t.Fatalf("upgraded authority revision=%q tree=%q ref=%q policy=%q", gotRevision, tree, baseRef, policy)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_worktrees SET start_policy='current_committed_head',pinned_base_tree=?,base_ref='HEAD' WHERE task_id=?`, strings.Repeat("b", 40), taskID.String()); err == nil {
		t.Fatal("upgraded historical base authority became mutable")
	}
}

func TestV31PopulatedRunGraphUpgradesThroughProjectSettingsAndCompletedState(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/v31-populated-run.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil || len(available) != 41 {
		t.Fatalf("migrations=%d err=%v", len(available), err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:31] {
		if migration.version == 19 || migration.version == 29 {
			err = applyParentTableRebuildMigration(ctx, db, migration)
		} else {
			err = applyMigration(ctx, db, migration)
		}
		if err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
	}
	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	roomID, taskID, charterID := domain.NewRoomID(), domain.NewTaskID(), domain.NewCharterID()
	runID, attemptID := domain.NewRunID(), domain.NewAttemptID()
	snapshotID, reportID, eventID := domain.NewContextSnapshotID(), domain.NewAgentReportID(), domain.NewEventID()
	canonical := []byte(`{"schema":"v31-preservation"}`)
	digest := sha256.Sum256(canonical)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at,project_id,ownership_kind) VALUES(?,?,?,?,'active',1,?,?,NULL,'legacy_standalone')`, []any{roomID.String(), "Historical", "preserve", t.TempDir(), now, now}},
		{`INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)`, []any{taskID.String(), roomID.String(), "Historical Task", "Preserve graph", now, now}},
		{`INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{charterID.String(), taskID.String(), "Preserve graph", "/legacy", "fake", "legacy", "patch", "owner", "migration-test", now}},
		{`INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, []any{snapshotID.String(), digest[:], canonical, canonical, now}},
		{`INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,last_event_sequence,created_at,updated_at,review_requested_at) VALUES(?,?,?,'awaiting_review',4,1,1,?,?,?)`, []any{runID.String(), taskID.String(), charterID.String(), now, now, now}},
		{`INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,adapter_id,state,created_at,terminal_at) VALUES(?,?,1,?,?,?,'output_submitted',?,?)`, []any{attemptID.String(), runID.String(), snapshotID.String(), digest[:], "fake", now, now}},
		{`INSERT INTO agent_reports(id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at) VALUES(?,?,?,?,?,?,?)`, []any{reportID.String(), runID.String(), attemptID.String(), "preserved", "preserved", []byte(`[]`), now}},
		{`INSERT INTO run_events(id,run_id,sequence,type,source,occurred_at,recorded_at,normalized_json) VALUES(?,?,1,'agent.report_submitted','migration-test',?,?,?)`, []any{eventID.String(), runID.String(), now, now, []byte(`{"state":"awaiting_review"}`)}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed v31 graph: %v\n%s", err, statement.query)
		}
	}
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 41 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for table, id := range map[string]string{"runs": runID.String(), "attempts": attemptID.String(), "agent_reports": reportID.String(), "run_events": eventID.String()} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s preservation count=%d err=%v", table, count, err)
		}
		rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			t.Fatal(err)
		}
		violated := rows.Next()
		_ = rows.Close()
		if violated {
			t.Fatalf("%s foreign key violation after v33", table)
		}
	}
}

func TestPopulatedV33UpgradesToV41PreservingProjectRunAndReviewHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "v33-populated.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	available, err := loadMigrations()
	if err != nil || len(available) != 41 {
		t.Fatalf("migrations=%d err=%v", len(available), err)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,checksum BLOB NOT NULL CHECK(length(checksum)=32),applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range available[:33] {
		if migration.version == 19 || migration.version == 29 {
			err = applyParentTableRebuildMigration(ctx, db, migration)
		} else {
			err = applyMigration(ctx, db, migration)
		}
		if err != nil {
			t.Fatalf("construct exact v33 with %s: %v", migration.name, err)
		}
	}

	now := time.Date(2026, 9, 9, 8, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	projectID, roomID, taskID := domain.NewProjectID(), domain.NewRoomID(), domain.NewTaskID()
	charterID, runID, attemptID := domain.NewCharterID(), domain.NewRunID(), domain.NewAttemptID()
	snapshotID, reportID := domain.NewContextSnapshotID(), domain.NewAgentReportID()
	artifactID, reviewID, eventID := domain.NewArtifactID(), domain.NewReviewDecisionID(), domain.NewEventID()
	canonical := []byte(`{"schema":"exact-v33","selected":[]}`)
	snapshotDigest := sha256.Sum256(canonical)
	patchDigest := sha256.Sum256([]byte("v33 patch evidence"))
	baseCommit := strings.Repeat("a", 40)
	baseTree := strings.Repeat("b", 40)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at,project_id,ownership_kind) VALUES(?,?,?,?,'active',1,?,?,?,'project')`, []any{roomID.String(), "V33 General", "preserved Room", "/v33/project", now, now, projectID.String()}},
		{`INSERT INTO projects(id,name,state,version,default_room_id,created_at,updated_at) VALUES(?,?,'active',1,?,?,?)`, []any{projectID.String(), "V33 Project", roomID.String(), now, now}},
		{`INSERT INTO project_repository_resources(project_id,name,local_locator,source_kind,clone_url,admitted_base,base_identity,target_worktree,dirty_admitted,state,version,created_at,updated_at) VALUES(?,?,?,'open','',?,?,'',1,'active',3,?,?)`, []any{projectID.String(), "V33 Repository", "/v33/repository", baseCommit, "sha:" + baseCommit + ":tree:" + baseTree, now, now}},
		{`INSERT INTO project_settings(project_id,version,writable_files_json,writable_directories_json,verification_commands_json,no_checks,updated_at) VALUES(?,2,'["README.md"]','["docs"]','[]',1,?)`, []any{projectID.String(), now}},
		{`INSERT INTO tasks(id,room_id,title,goal,state,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)`, []any{taskID.String(), roomID.String(), "V33 Task", "Preserve exact schema 33 history", now, now}},
		{`INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{charterID.String(), taskID.String(), "Preserve exact schema 33 history", "/v33/project", "fake", "legacy", "patch", "owner", "migration-fixture", now}},
		{`INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,?,?)`, []any{snapshotID.String(), snapshotDigest[:], canonical, canonical, now}},
		{`INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,last_event_sequence,created_at,updated_at,terminal_at) VALUES(?,?,?,'completed',8,1,1,?,?,?)`, []any{runID.String(), taskID.String(), charterID.String(), now, now, now}},
		{`INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,adapter_id,state,created_at,terminal_at) VALUES(?,?,1,?,?,?,'output_submitted',?,?)`, []any{attemptID.String(), runID.String(), snapshotID.String(), snapshotDigest[:], "fake", now, now}},
		{`INSERT INTO agent_reports(id,run_id,attempt_id,summary,final_text,claimed_checks_json,completed_at) VALUES(?,?,?,?,?,'[]',?)`, []any{reportID.String(), runID.String(), attemptID.String(), "V33 result", "preserved result evidence", now}},
		{`INSERT INTO artifacts(id,run_id,attempt_id,kind,locator,digest,media_type,description,position,role,created_at) VALUES(?,?,?,'patch','runtime/v33.patch',?,'text/x-diff','preserved patch',0,'output',?)`, []any{artifactID.String(), runID.String(), attemptID.String(), patchDigest[:], now}},
		{`INSERT INTO review_decisions(id,run_id,kind,expected_run_version,reviewer_note,decided_at) VALUES(?,?,'accept',8,'preserved human review',?)`, []any{reviewID.String(), runID.String(), now}},
		{`INSERT INTO review_artifacts(review_id,position,artifact_link) VALUES(?,0,?)`, []any{reviewID.String(), "artifact:" + artifactID.String()}},
		{`INSERT INTO run_events(id,run_id,sequence,type,source,occurred_at,recorded_at,normalized_json) VALUES(?,?,1,'run.completed','app',?,?,?)`, []any{eventID.String(), runID.String(), now, now, []byte(`{"state":"completed","version":8}`)}},
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed exact v33: %v\n%s", err, statement.query)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&beforeVersion); err != nil || beforeVersion != 33 {
		t.Fatalf("fixture is not exact schema 33: version=%d err=%v", beforeVersion, err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var currentVersion int
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&currentVersion); err != nil || currentVersion != 41 {
		t.Fatalf("version=%d err=%v", currentVersion, err)
	}
	assertions := []struct {
		query string
		args  []any
		want  []any
	}{
		{`SELECT name,state,version,default_room_id,description FROM projects WHERE id=?`, []any{projectID.String()}, []any{"V33 Project", "active", int64(1), roomID.String(), ""}},
		{`SELECT name,ownership_kind,project_id FROM rooms WHERE id=?`, []any{roomID.String()}, []any{"V33 General", "project", projectID.String()}},
		{`SELECT title,goal,state FROM tasks WHERE id=?`, []any{taskID.String()}, []any{"V33 Task", "Preserve exact schema 33 history", "open"}},
		{`SELECT state,version,current_attempt_number FROM runs WHERE id=?`, []any{runID.String()}, []any{"completed", int64(8), int64(1)}},
		{`SELECT state,context_snapshot_id,hex(context_digest) FROM attempts WHERE id=?`, []any{attemptID.String()}, []any{"output_submitted", snapshotID.String(), strings.ToUpper(fmt.Sprintf("%x", snapshotDigest))}},
		{`SELECT summary,final_text FROM agent_reports WHERE id=?`, []any{reportID.String()}, []any{"V33 result", "preserved result evidence"}},
		{`SELECT locator,hex(digest),description,role FROM artifacts WHERE id=?`, []any{artifactID.String()}, []any{"runtime/v33.patch", strings.ToUpper(fmt.Sprintf("%x", patchDigest)), "preserved patch", "output"}},
		{`SELECT kind,expected_run_version,comment FROM review_decisions WHERE id=?`, []any{reviewID.String()}, []any{"accept", int64(8), "preserved human review"}},
		{`SELECT admitted_base,base_identity,state,version FROM project_repository_resources WHERE project_id=?`, []any{projectID.String()}, []any{baseCommit, "sha:" + baseCommit + ":tree:" + baseTree, "active", int64(3)}},
		{`SELECT legacy_commit,legacy_tree,identity_source FROM repositories WHERE repo_id=?`, []any{"repo_" + strings.TrimPrefix(projectID.String(), "project_")}, []any{baseCommit, baseTree, "legacy_unverified"}},
	}
	for _, assertion := range assertions {
		values := make([]any, len(assertion.want))
		pointers := make([]any, len(values))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err = db.QueryRowContext(ctx, assertion.query, assertion.args...).Scan(pointers...); err != nil {
			t.Fatalf("preservation query failed: %v\n%s", err, assertion.query)
		}
		if !reflect.DeepEqual(values, assertion.want) {
			t.Fatalf("preservation values=%#v want=%#v\n%s", values, assertion.want, assertion.query)
		}
	}
	for _, table := range []string{"task_resource_snapshots", "resource_result_groups", "result_closures"} {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration invented %s rows: count=%d err=%v", table, count, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated schema 41: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
