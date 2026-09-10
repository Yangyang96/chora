package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"github.com/Yangyang96/chora/internal/store/sqlite"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	_ "modernc.org/sqlite"
)

func TestProjectArchiveRefusesAwaitingReviewAcrossOwnedRoom(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	room, _ := insertRoomAndBinding(t, db, seeded.now, t.TempDir())
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "task", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{seeded.revision.RevisionID()}, WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "fake", SandboxMode: "workspace", ExpectedOutput: "patch", ResponsibleHuman: "owner", CapabilityEnvelope: domain.CapabilityEnvelope{"shell": true}, Initiator: "test", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := domain.NewAgentRun(domain.NewRunID(), task.ID(), charter.ID(), seeded.now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		return tx.InsertRun(ctx, run)
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, command := range []domain.CommandKind{domain.CommandPrepareRun, domain.CommandStartAttempt, domain.CommandSubmitAgentReportDirectReview} {
		next, err := run.Transition(command, seeded.now.Add(time.Duration(i+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(ctx, run.Version(), next) })
		if err != nil {
			t.Fatal(err)
		}
		run = next
	}
	project, err := db.Reader().GetProject(ctx, room.ProjectID())
	if err != nil {
		t.Fatal(err)
	}
	archived, err := project.Archive(project.Version(), seeded.now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveProjectCAS(ctx, project.Version(), archived) })
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("archive error=%v", err)
	}
	currentRoom, err := db.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	roomArchived, err := currentRoom.Archive(currentRoom.Version(), seeded.now.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	event := storecontract.RoomLifecycleEvent{RoomID: room.ID(), FromState: currentRoom.State(), ToState: roomArchived.State(), Version: roomArchived.Version(), ActorID: "owner", SessionID: "session", IdempotencyKeyHash: sha256.Sum256([]byte("archive-room-key")), RequestDigest: sha256.Sum256([]byte("archive-room-request")), OccurredAt: roomArchived.UpdatedAt()}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), roomArchived, event)
	})
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("Room archive error=%v", err)
	}
	review, err := domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: domain.NewReviewDecisionID(), RunID: run.ID(), Kind: domain.ReviewDecisionAccept, ExpectedRunVersion: run.Version(), DecidedAt: seeded.now.Add(12 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := run.ApplyReview(review)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(ctx, run.Version(), accepted) }); err != nil {
		t.Fatal(err)
	}
	currentRoom, err = db.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	roomArchived, err = currentRoom.Archive(currentRoom.Version(), seeded.now.Add(13*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	event = storecontract.RoomLifecycleEvent{RoomID: room.ID(), FromState: currentRoom.State(), ToState: roomArchived.State(), Version: roomArchived.Version(), ActorID: "owner", SessionID: "session", IdempotencyKeyHash: sha256.Sum256([]byte("archive-accepted-key")), RequestDigest: sha256.Sum256([]byte("archive-accepted-request")), OccurredAt: roomArchived.UpdatedAt()}
	err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), roomArchived, event)
	})
	if !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("accepted open Room archive error=%v", err)
	}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.CloseTaskForAcceptedRun(ctx, task.ID(), accepted.ID(), accepted.Version())
	}); err != nil {
		t.Fatal(err)
	}
	// Exercise Apply states independently of the accepted/open predicate. The
	// authority trigger is disabled only in this test-owned database so the
	// fixture can focus on the Room/Project lifecycle guard without building a
	// complete verified-review graph (covered by local_review_results_test.go).
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	digest := sha256.Sum256([]byte("archive guard digest"))
	if _, err = raw.ExecContext(ctx, `DROP TRIGGER patch_applications_authority`); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO patch_applications(
run_id,review_decision_id,patch_digest,declared_files_digest,target_identity,base_revision,
affected_paths_json,pre_state_digest,state,version,reason,started_at,updated_at
) VALUES(?,?,?,?,?,?,?,?, 'applying',1,'',?,?)`, accepted.ID().String(), domain.NewReviewDecisionID().String(), digest[:], digest[:], "checkout", "base", []byte(`["README.md"]`), digest[:], seeded.now.Add(15*time.Second).Format(time.RFC3339Nano), seeded.now.Add(15*time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	assertRoomArchiveBlocked := func(key string, at time.Time) {
		t.Helper()
		current, loadErr := db.Reader().GetRoom(ctx, room.ID())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		next, archiveErr := current.Archive(current.Version(), at)
		if archiveErr != nil {
			t.Fatal(archiveErr)
		}
		nextEvent := storecontract.RoomLifecycleEvent{RoomID: room.ID(), FromState: current.State(), ToState: next.State(), Version: next.Version(), ActorID: "owner", SessionID: "session", IdempotencyKeyHash: sha256.Sum256([]byte(key)), RequestDigest: sha256.Sum256([]byte(key + "-request")), OccurredAt: next.UpdatedAt()}
		archiveErr = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			return tx.SaveRoomLifecycleCAS(ctx, current.Version(), next, nextEvent)
		})
		if !errors.Is(archiveErr, storecontract.ErrRoomStateForbidden) {
			t.Fatalf("%s Room archive error=%v", key, archiveErr)
		}
	}
	assertRoomArchiveBlocked("archive-applying", seeded.now.Add(16*time.Second))
	if _, err = raw.ExecContext(ctx, `UPDATE patch_applications SET state='recovery_required',version=2,reason='interrupted',updated_at=? WHERE run_id=?`, seeded.now.Add(17*time.Second).Format(time.RFC3339Nano), accepted.ID().String()); err != nil {
		t.Fatal(err)
	}
	assertRoomArchiveBlocked("archive-recovery", seeded.now.Add(18*time.Second))
	if _, err = raw.ExecContext(ctx, `UPDATE patch_applications SET state='applying',version=3,reason='',started_at=?,updated_at=? WHERE run_id=?`, seeded.now.Add(19*time.Second).Format(time.RFC3339Nano), seeded.now.Add(19*time.Second).Format(time.RFC3339Nano), accepted.ID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, `UPDATE patch_applications SET state='applied',version=4,post_state_digest=?,updated_at=?,applied_at=? WHERE run_id=?`, digest[:], seeded.now.Add(20*time.Second).Format(time.RFC3339Nano), seeded.now.Add(20*time.Second).Format(time.RFC3339Nano), accepted.ID().String()); err != nil {
		t.Fatal(err)
	}
	currentRoom, err = db.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	roomArchived, err = currentRoom.Archive(currentRoom.Version(), seeded.now.Add(21*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	event = storecontract.RoomLifecycleEvent{RoomID: room.ID(), FromState: currentRoom.State(), ToState: roomArchived.State(), Version: roomArchived.Version(), ActorID: "owner", SessionID: "session", IdempotencyKeyHash: sha256.Sum256([]byte("archive-closed-key")), RequestDigest: sha256.Sum256([]byte("archive-closed-request")), OccurredAt: roomArchived.UpdatedAt()}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveRoomLifecycleCAS(ctx, currentRoom.Version(), roomArchived, event)
	}); err != nil {
		t.Fatalf("closed legacy accepted Room archive: %v", err)
	}
	project, err = db.Reader().GetProject(ctx, room.ProjectID())
	if err != nil {
		t.Fatal(err)
	}
	archived, err = project.Archive(project.Version(), seeded.now.Add(22*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveProjectCAS(ctx, project.Version(), archived) }); err != nil {
		t.Fatalf("closed legacy accepted Project archive: %v", err)
	}
}

func TestProjectRoomResourceAssociationsAreImmutableAndFailClosed(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	room, _ := insertRoomAndBinding(t, db, seeded.now, t.TempDir())
	unclassified, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "Quarantined", Description: "unknown provenance", WorkspaceRoot: t.TempDir(), CreatedAt: seeded.now, UpdatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRoom(ctx, unclassified) }); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	otherProject := domain.NewProjectID()
	if _, err = raw.ExecContext(ctx, `UPDATE rooms SET project_id=?,version=version+1 WHERE id=?`, otherProject.String(), room.ID().String()); err == nil {
		t.Fatal("Room Project reassignment succeeded")
	}
	if _, err = raw.ExecContext(ctx, `UPDATE rooms SET project_id=?,ownership_kind='project',version=version+1 WHERE id=?`, room.ProjectID().String(), unclassified.ID().String()); err == nil {
		t.Fatal("unclassified Room ownership upgrade succeeded")
	}
	if _, err = raw.ExecContext(ctx, `UPDATE project_repository_resources SET name='changed',version=version+1 WHERE project_id=?`, room.ProjectID().String()); err == nil {
		t.Fatal("resource admission name changed")
	}
	if _, err = raw.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, room.ProjectID().String()); err == nil {
		t.Fatal("Project deletion succeeded")
	}
	if _, err = raw.ExecContext(ctx, `INSERT INTO projects(id,name,state,version,default_room_id,created_at,updated_at) VALUES(?,?,'active',1,?,?,?)`, otherProject.String(), "Mismatched", room.ID().String(), seeded.now.Format(time.RFC3339Nano), seeded.now.Format(time.RFC3339Nano)); err == nil {
		t.Fatal("Project accepted another Project's default Room")
	}
}

func TestProjectDescriptionPersistsIndependentlyAcrossRenameAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "description.sqlite")
	db, err := openLatestSQLiteTestStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	roomID := domain.NewRoomID()
	project, err := domain.NewProject(domain.ProjectParams{ID: domain.NewProjectID(), Name: "Long lived project", Description: "Shared project purpose", DefaultRoomID: roomID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	room, err := domain.NewRoom(domain.RoomParams{ID: roomID, ProjectID: project.ID(), OwnershipKind: domain.RoomOwnershipProject, Name: "General", Description: "General discussion", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertRoom(ctx, room)
	}); err != nil {
		t.Fatal(err)
	}
	renamed, err := project.Rename("New name", project.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveProjectCAS(ctx, project.Version(), renamed) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Reader().GetProject(ctx, project.ID())
	if err != nil || got.Description() != "Shared project purpose" || got.Name() != "New name" {
		t.Fatalf("project=%+v err=%v", got, err)
	}
	general, err := reopened.Reader().GetRoom(ctx, roomID)
	if err != nil || general.Description() != "General discussion" {
		t.Fatalf("room=%+v err=%v", general, err)
	}
}
